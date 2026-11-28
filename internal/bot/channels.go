package bot

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/channel/wechat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

var ErrCredentialInUse = errors.New("channel credential already in use")

type Channels struct {
	Telegram Telegram `json:"telegram"`
	Wechat   Wechat   `json:"wechat"`
	Feishu   Feishu   `json:"feishu"`
}

type channelSession struct {
	login           *wechat.Login
	loginID         string
	feishuSession   *feishuSession
	feishuSessionID string
}

// channelSession requires the bot lock.
func (s *Service) channelSession(botID string) *channelSession {
	if s.sessions == nil {
		s.sessions = make(map[string]*channelSession)
	}
	if s.sessions[botID] == nil {
		s.sessions[botID] = &channelSession{}
	}
	return s.sessions[botID]
}

func (s *Service) Channels(ctx context.Context, botID string) (Channels, error) {
	channels, err := s.channels(ctx, botID)
	if err == nil {
		channels.Telegram.ManagerConfigured, channels.Telegram.ManagedCreationAvailable = s.managerStatus()
	}
	return channels, err
}

// channels leaves out the web setup capability so callers can hold the settings lock.
func (s *Service) channels(ctx context.Context, botID string) (Channels, error) {
	tg := Telegram{AllowedUsers: []int64{}, Status: channel.Status{State: "off"}, Transport: "polling"}
	row, err := s.q.GetTelegramSettings(ctx, botID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Channels{}, err
	}
	if err == nil {
		tg.Enabled = true
		tg.BotID, _, _ = strings.Cut(row.Token, ":")
		tg.AllowedUsers, err = parseUsers(row.AllowedUsers)
		if err != nil {
			return Channels{}, err
		}
		if err := json.Unmarshal([]byte(row.Options), &tg.Options); err != nil {
			return Channels{}, err
		}
		if row.WebhookUrl != "" {
			tg.Transport, tg.WebhookURL = "webhook", row.WebhookUrl
		}
	}
	if s.ChannelStatus != nil {
		tg.Status = s.ChannelStatus(botID, "telegram")
	}
	wx, err := s.wechatView(ctx, botID)
	if err != nil {
		return Channels{}, err
	}
	fs, err := s.GetFeishu(ctx, botID)
	return Channels{Telegram: tg, Wechat: wx, Feishu: fs}, err
}

// writeActiveChannel orders a channel write with an archive, which does not take s.mu.
// Take the SQLite writer first so other writes cannot invalidate the read snapshot.
func (s *Service) writeActiveChannel(ctx context.Context, botID string, write func(*sqlc.Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	if err := q.LockChannelWriter(ctx); err != nil {
		return err
	}
	if err := db.RequireActive(ctx, q, botID); err != nil {
		return err
	}
	if err := write(q); err != nil {
		return err
	}
	return tx.Commit()
}

// canStartChannel also guards recovery after a failed write. A canceled request
// must still let an active bot restart with its previous credentials.
func (s *Service) canStartChannel(ctx context.Context, botID string) bool {
	err := db.RequireActive(context.WithoutCancel(ctx), s.q, botID)
	if err != nil && !errors.Is(err, db.ErrBotArchived) {
		slog.WarnContext(ctx, "check bot before starting channel", "agent", botID, "err", err)
	}
	return err == nil
}

func (s *Service) checkCredential(ctx context.Context, botID, platform, credential string) error {
	var id, name string
	var err error
	switch platform {
	case "telegram":
		account, _, _ := strings.Cut(credential, ":")
		return s.checkTelegramAccount(ctx, botID, account)
	case "wechat":
		owner, e := s.q.WechatCredentialOwner(ctx, credential)
		id, name, err = owner.ID, owner.Name, e
	case "feishu":
		owner, e := s.q.FeishuCredentialOwner(ctx, credential)
		id, name, err = owner.ID, owner.Name, e
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if id != botID {
		return fmt.Errorf("%w: %s is used by bot %q (%s)", ErrCredentialInUse, platform, name, id)
	}
	return nil
}
