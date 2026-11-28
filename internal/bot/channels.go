package bot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"strings"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/channel/wechat"
)

var ErrCredentialInUse = errors.New("channel credential already in use")

// feishuSetup is the Feishu capability that bot needs until channel coordination moves out.
type feishuSetup interface {
	State(ctx context.Context, botID string) (state string, paired bool, err error)
	Owner(ctx context.Context, botID string) (account, owner string, err error)
	FingerprintLocked(ctx context.Context, botID string) ([sha256.Size]byte, error)
	DisconnectLocked(ctx context.Context, botID string) error
	RevokePairingLocked(ctx context.Context, botID string) error
	PrepareStartLocked(ctx context.Context, botID string) (func(context.Context), error)
	CancelRegistrationLocked(botID string)
	StopLocked(botID string)
}

// BindFeishu connects channel coordination before the service starts.
func (s *Service) BindFeishu(setup feishuSetup) { s.feishu = setup }

type channelSession struct {
	login   *wechat.Login
	loginID string
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

// ChannelViews returns the public views that bot owns.
func (s *Service) ChannelViews(ctx context.Context, botID string) (Telegram, Wechat, error) {
	tg, wx, err := s.channelViews(ctx, botID)
	if err == nil {
		tg.ManagerConfigured, tg.ManagedCreationAvailable = s.managerStatus()
	}
	return tg, wx, err
}

// channelViews leaves out the web setup capability so callers can hold the settings lock.
func (s *Service) channelViews(ctx context.Context, botID string) (Telegram, Wechat, error) {
	tg := Telegram{AllowedUsers: []int64{}, Status: channel.Status{State: "off"}, Transport: "polling"}
	row, err := s.q.GetTelegramSettings(ctx, botID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Telegram{}, Wechat{}, err
	}
	if err == nil {
		tg.Enabled = true
		tg.BotID, _, _ = strings.Cut(row.Token, ":")
		tg.AllowedUsers, err = parseUsers(row.AllowedUsers)
		if err != nil {
			return Telegram{}, Wechat{}, err
		}
		if err := json.Unmarshal([]byte(row.Options), &tg.Options); err != nil {
			return Telegram{}, Wechat{}, err
		}
		if row.WebhookUrl != "" {
			tg.Transport, tg.WebhookURL = "webhook", row.WebhookUrl
		}
	}
	if s.ChannelStatus != nil {
		tg.Status = s.ChannelStatus(botID, "telegram")
	}
	wx, err := s.wechatView(ctx, botID)
	return tg, wx, err
}
