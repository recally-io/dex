package bot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"uuid"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/channel/wechat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

// Wechat is the public account view. Credentials and runtime tokens stay private.
type Wechat struct {
	Enabled bool           `json:"enabled"`
	BotID   string         `json:"botId,omitempty"`
	Owner   string         `json:"owner,omitempty"`
	Status  channel.Status `json:"status"`
}

var verifyDigits = regexp.MustCompile(`^[0-9]{1,16}$`)

// ConfigureWechat connects the runtime before the HTTP server starts.
func (s *Service) ConfigureWechat(options wechat.Options, onChange func(string, *wechat.Config)) {
	s.wechatOptions = options
	s.onWechat = onChange
}

// WechatConfig returns the saved account, or nil when WeChat is off.
func (s *Service) WechatConfig(ctx context.Context, botID string) (*wechat.Config, error) {
	row, err := s.q.GetWechatSettings(ctx, botID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := &wechat.Config{Token: row.Token, BaseURL: row.BaseUrl, BotID: row.WechatBotID, Owner: row.Owner}
	if err = cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (s *Service) wechatView(ctx context.Context, botID string) (Wechat, error) {
	view := Wechat{Status: channel.Status{State: "off"}}
	cfg, err := s.WechatConfig(ctx, botID)
	if err != nil {
		return view, err
	}
	if cfg != nil {
		view.Enabled = true
		view.BotID = cfg.BotID
		view.Owner = cfg.Owner
	}
	if s.ChannelStatus != nil {
		view.Status = s.ChannelStatus(botID, "wechat")
	}
	return view, nil
}

// BeginWechatLogin replaces a temporary QR session. No credential is stored yet.
func (s *Service) BeginWechatLogin(ctx context.Context, botID string) (wechat.LoginView, error) {
	s.mu.Lock()
	id := uuid.NewV7().String()
	s.channelSession(botID).loginID = id
	s.channelSession(botID).login = nil
	s.mu.Unlock()
	login, view, err := wechat.BeginLogin(ctx, id, s.wechatOptions)
	if err != nil {
		return view, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.channelSession(botID).loginID {
		return view, fmt.Errorf("%w: WeChat login was replaced", ErrInvalid)
	}
	s.channelSession(botID).login = login
	return view, nil
}

// PollWechatLogin saves credentials only if the same session is still current after polling.
func (s *Service) PollWechatLogin(ctx context.Context, botID string, id, code string) (wechat.LoginView, error) {
	if code != "" && !verifyDigits.MatchString(code) {
		return wechat.LoginView{}, fmt.Errorf("%w: enter the digits shown on your phone", ErrInvalid)
	}
	s.mu.Lock()
	login := s.channelSession(botID).login
	currentID := s.channelSession(botID).loginID
	s.mu.Unlock()
	if login == nil || id != currentID {
		return wechat.LoginView{}, fmt.Errorf("%w: start a new WeChat login", ErrInvalid)
	}
	view, err := login.Poll(ctx, code)
	if err != nil {
		return view, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channelSession(botID).login != login {
		return wechat.LoginView{}, fmt.Errorf("%w: WeChat login was replaced", ErrInvalid)
	}
	return login.Activate(func(cfg *wechat.Config) error {
		return s.activateWechat(ctx, botID, cfg)
	})
}

func (s *Service) activateWechat(ctx context.Context, botID string, cfg *wechat.Config) error {
	owner, err := s.q.WechatCredentialOwner(ctx, cfg.BotID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && owner.ID != botID {
		return fmt.Errorf("%w: wechat is used by bot %q (%s)", ErrCredentialInUse, owner.Name, owner.ID)
	}
	previous, err := s.WechatConfig(ctx, botID)
	if err != nil {
		return err
	}
	// Join all old requests before resetting runtime state shared with the replacement.
	s.onWechat(botID, nil)
	if err := db.WriteActiveChannel(ctx, s.db, botID, func(q *sqlc.Queries) error {
		if err := q.SetWechatSettings(ctx, sqlc.SetWechatSettingsParams{BotID: botID, Token: cfg.Token, BaseUrl: cfg.BaseURL, WechatBotID: cfg.BotID, Owner: cfg.Owner}); err != nil {
			return err
		}
		if _, err := q.EnsureWechatRuntime(ctx, sqlc.EnsureWechatRuntimeParams{BotID: cfg.BotID, Owner: cfg.Owner}); err != nil {
			return err
		}
		return q.SetWechatPause(ctx, sqlc.SetWechatPauseParams{BotID: cfg.BotID, Owner: cfg.Owner, PauseUntil: 0})
	}); err != nil {
		if previous != nil && db.CanStartChannel(ctx, s.q, botID) {
			s.onWechat(botID, previous)
		}
		return err
	}
	if db.CanStartChannel(ctx, s.q, botID) {
		s.onWechat(botID, cfg)
	}
	return nil
}

// DeleteWechat stops the bot, forgets credentials, and invalidates any QR poll in flight.
func (s *Service) DeleteWechat(ctx context.Context, botID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteWechat(ctx, botID)
}

func (s *Service) deleteWechat(ctx context.Context, botID string) error {
	s.channelSession(botID).login = nil
	s.channelSession(botID).loginID = ""
	previous, err := s.WechatConfig(ctx, botID)
	if err != nil {
		return err
	}
	s.onWechat(botID, nil)
	if err := s.q.DeleteWechatSettings(ctx, botID); err != nil {
		if previous != nil && db.CanStartChannel(ctx, s.q, botID) {
			s.onWechat(botID, previous)
		}
		return err
	}
	return nil
}
