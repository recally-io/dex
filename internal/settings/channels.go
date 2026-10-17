package settings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/wechat"
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

// channelSession requires the settings lock.
func (s *Service) channelSession(agentID string) *channelSession {
	if s.sessions == nil {
		s.sessions = make(map[string]*channelSession)
	}
	if s.sessions[agentID] == nil {
		s.sessions[agentID] = &channelSession{}
	}
	return s.sessions[agentID]
}

func (s *Service) Channels(ctx context.Context, agentID string) (Channels, error) {
	tg := Telegram{AllowedUsers: []int64{}, Status: channel.Status{State: "off"}}
	cfg, err := s.TelegramConfig(ctx, agentID)
	if err != nil {
		return Channels{}, err
	}
	if cfg != nil {
		tg.Enabled = true
		tg.BotID, _, _ = strings.Cut(cfg.Token, ":")
		tg.AllowedUsers = cfg.AllowedUsers
	}
	if s.ChannelStatus != nil {
		tg.Status = s.ChannelStatus(agentID, "telegram")
	}
	wx, err := s.wechatView(ctx, agentID)
	if err != nil {
		return Channels{}, err
	}
	fs, err := s.GetFeishu(ctx, agentID)
	return Channels{Telegram: tg, Wechat: wx, Feishu: fs}, err
}

func (s *Service) checkCredential(ctx context.Context, agentID, platform, credential string) error {
	var id, name string
	var err error
	switch platform {
	case "telegram":
		owner, e := s.q.TelegramCredentialOwner(ctx, credential)
		id, name, err = owner.ID, owner.Name, e
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
	if id != agentID {
		return fmt.Errorf("%w: %s is used by agent %q (%s)", ErrCredentialInUse, platform, name, id)
	}
	return nil
}
