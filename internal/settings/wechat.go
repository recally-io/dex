package settings

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
func (s *Service) ConfigureWechat(options wechat.Options, conn *sql.DB, onChange func(*wechat.Config)) {
	s.wechatOptions = options
	s.wechatDB = conn
	s.onWechat = onChange
}

// WechatConfig returns the saved account, or nil when WeChat is off.
func (s *Service) WechatConfig(ctx context.Context) (*wechat.Config, error) {
	row, err := s.q.GetWechatSettings(ctx)
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

func (s *Service) wechatView(ctx context.Context) (Wechat, error) {
	view := Wechat{Status: channel.Status{State: "off"}}
	cfg, err := s.WechatConfig(ctx)
	if err != nil {
		return view, err
	}
	if cfg != nil {
		view.Enabled = true
		view.BotID = cfg.BotID
		view.Owner = cfg.Owner
	}
	if s.ChannelStatus != nil {
		view.Status = s.ChannelStatus("wechat")
	}
	return view, nil
}

// BeginWechatLogin replaces a temporary QR session. No credential is stored yet.
func (s *Service) BeginWechatLogin(ctx context.Context) (wechat.LoginView, error) {
	s.mu.Lock()
	id := uuid.NewV7().String()
	s.loginID = id
	s.login = nil
	s.mu.Unlock()
	login, view, err := wechat.BeginLogin(ctx, id, s.wechatOptions)
	if err != nil {
		return view, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.loginID {
		return view, fmt.Errorf("%w: WeChat login was replaced", ErrInvalid)
	}
	s.login = login
	return view, nil
}

// PollWechatLogin saves credentials only if the same session is still current after polling.
func (s *Service) PollWechatLogin(ctx context.Context, id, code string) (wechat.LoginView, error) {
	if code != "" && !verifyDigits.MatchString(code) {
		return wechat.LoginView{}, fmt.Errorf("%w: enter the digits shown on your phone", ErrInvalid)
	}
	s.mu.Lock()
	login := s.login
	currentID := s.loginID
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
	if s.login != login {
		return wechat.LoginView{}, fmt.Errorf("%w: WeChat login was replaced", ErrInvalid)
	}
	return login.Activate(func(cfg *wechat.Config) error {
		return s.activateWechat(ctx, cfg)
	})
}

func (s *Service) activateWechat(ctx context.Context, cfg *wechat.Config) error {
	previous, err := s.WechatConfig(ctx)
	if err != nil {
		return err
	}
	// Join all old requests before resetting runtime state shared with the replacement.
	s.onWechat(nil)
	activated := false
	defer func() {
		if !activated {
			s.onWechat(previous)
		}
	}()
	tx, err := s.wechatDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	if err = q.SetWechatSettings(ctx, sqlc.SetWechatSettingsParams{Token: cfg.Token, BaseUrl: cfg.BaseURL, BotID: cfg.BotID, Owner: cfg.Owner}); err != nil {
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
	s.onWechat(cfg)
	activated = true
	return nil
}

// DeleteWechat stops the bot, forgets credentials, and invalidates any QR poll in flight.
func (s *Service) DeleteWechat(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.login = nil
	s.loginID = ""
	if err := s.q.DeleteWechatSettings(ctx); err != nil {
		return err
	}
	s.onWechat(nil)
	return nil
}
