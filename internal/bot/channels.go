package bot

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/channel/wechat"
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
