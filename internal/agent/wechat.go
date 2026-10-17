package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"uuid"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/wechat"
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
func (s *Service) ConfigureWechat(options wechat.Options, conn *sql.DB, onChange func(string, *wechat.Config)) {
	s.wechatOptions = options
	s.wechatDB = conn
	s.onWechat = onChange
}

// WechatConfig returns the saved account, or nil when WeChat is off.
func (s *Service) WechatConfig(ctx context.Context, agentID string) (*wechat.Config, error) {
	row, err := s.q.GetWechatSettings(ctx, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := &wechat.Config{Token: row.Token, BaseURL: row.BaseUrl, BotID: row.BotID, Owner: row.Owner}
	if err = cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (s *Service) wechatView(ctx context.Context, agentID string) (Wechat, error) {
	view := Wechat{Status: channel.Status{State: "off"}}
	cfg, err := s.WechatConfig(ctx, agentID)
	if err != nil {
		return view, err
	}
	if cfg != nil {
		view.Enabled = true
		view.BotID = cfg.BotID
		view.Owner = cfg.Owner
	}
	if s.ChannelStatus != nil {
		view.Status = s.ChannelStatus(agentID, "wechat")
	}
	return view, nil
}

// BeginWechatLogin replaces a temporary QR session. No credential is stored yet.
func (s *Service) BeginWechatLogin(ctx context.Context, agentID string) (wechat.LoginView, error) {
	s.mu.Lock()
	id := uuid.NewV7().String()
	s.channelSession(agentID).loginID = id
	s.channelSession(agentID).login = nil
	s.mu.Unlock()
	login, view, err := wechat.BeginLogin(ctx, id, s.wechatOptions)
	if err != nil {
		return view, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.channelSession(agentID).loginID {
		return view, fmt.Errorf("%w: WeChat login was replaced", ErrInvalid)
	}
	s.channelSession(agentID).login = login
	return view, nil
}

// PollWechatLogin saves credentials only if the same session is still current after polling.
func (s *Service) PollWechatLogin(ctx context.Context, agentID string, id, code string) (wechat.LoginView, error) {
	if code != "" && !verifyDigits.MatchString(code) {
		return wechat.LoginView{}, fmt.Errorf("%w: enter the digits shown on your phone", ErrInvalid)
	}
	s.mu.Lock()
	login := s.channelSession(agentID).login
	currentID := s.channelSession(agentID).loginID
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
	if s.channelSession(agentID).login != login {
		return wechat.LoginView{}, fmt.Errorf("%w: WeChat login was replaced", ErrInvalid)
	}
	return login.Activate(func(cfg *wechat.Config) error {
		return s.activateWechat(ctx, agentID, cfg)
	})
}

func (s *Service) activateWechat(ctx context.Context, agentID string, cfg *wechat.Config) error {
	if err := s.checkCredential(ctx, agentID, "wechat", cfg.BotID); err != nil {
		return err
	}
	previous, err := s.WechatConfig(ctx, agentID)
	if err != nil {
		return err
	}
	// Join all old requests before resetting runtime state shared with the replacement.
	s.onWechat(agentID, nil)
	activated := false
	defer func() {
		if !activated {
			s.onWechat(agentID, previous)
		}
	}()
	tx, err := s.wechatDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	if err = q.SetWechatSettings(ctx, sqlc.SetWechatSettingsParams{AgentID: agentID, Token: cfg.Token, BaseUrl: cfg.BaseURL, BotID: cfg.BotID, Owner: cfg.Owner}); err != nil {
		return err
	}
	if _, err = q.EnsureWechatRuntime(ctx, sqlc.EnsureWechatRuntimeParams{BotID: cfg.BotID, Owner: cfg.Owner}); err != nil {
		return err
	}
	if err = q.SetWechatPause(ctx, sqlc.SetWechatPauseParams{BotID: cfg.BotID, Owner: cfg.Owner, PauseUntil: 0}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.onWechat(agentID, cfg)
	activated = true
	return nil
}

// DeleteWechat stops the bot, forgets credentials, and invalidates any QR poll in flight.
func (s *Service) DeleteWechat(ctx context.Context, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channelSession(agentID).login = nil
	s.channelSession(agentID).loginID = ""
	if err := s.q.DeleteWechatSettings(ctx, agentID); err != nil {
		return err
	}
	s.onWechat(agentID, nil)
	return nil
}
