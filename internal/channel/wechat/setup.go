package wechat

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"uuid"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

var (
	ErrInvalid         = errors.New("invalid settings")
	ErrCredentialInUse = errors.New("channel credential already in use")
	verifyDigits       = regexp.MustCompile(`^[0-9]{1,16}$`)
)

// Wechat is the public account view. Credentials and runtime tokens stay private.
type Wechat struct {
	Enabled bool           `json:"enabled"`
	BotID   string         `json:"botId,omitempty"`
	Owner   string         `json:"owner,omitempty"`
	Status  channel.Status `json:"status"`
}

type loginSession struct {
	id        string
	login     *qrLogin
	confirmed LoginView
}

// SetupService owns saved settings and in-flight QR logins for WeChat.
// The app shares mu with the channel coordinator. Locked methods require that lock.
type SetupService struct {
	q        *sqlc.Queries
	db       *sql.DB
	mu       *sync.Mutex
	options  Options
	onChange func(string, *Config)
	status   func(string) channel.Status
	sessions map[string]*loginSession
}

// NewSetup connects storage, the settings lock, and the runtime callbacks.
func NewSetup(q *sqlc.Queries, conn *sql.DB, mu *sync.Mutex, options Options, onChange func(string, *Config), status func(string) channel.Status) *SetupService {
	return &SetupService{q: q, db: conn, mu: mu, options: options, onChange: onChange, status: status, sessions: make(map[string]*loginSession)}
}

// Config returns the saved account, or nil when WeChat is off.
func (s *SetupService) Config(ctx context.Context, botID string) (*Config, error) {
	row, err := s.q.GetWechatSettings(ctx, botID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := &Config{Token: row.Token, BaseURL: row.BaseUrl, BotID: row.WechatBotID, Owner: row.Owner}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// View returns the public account and runtime status.
func (s *SetupService) View(ctx context.Context, botID string) (Wechat, error) {
	view := Wechat{Status: channel.Status{State: "off"}}
	cfg, err := s.Config(ctx, botID)
	if err != nil {
		return view, err
	}
	if cfg != nil {
		view.Enabled = true
		view.BotID = cfg.BotID
		view.Owner = cfg.Owner
	}
	if s.status != nil {
		view.Status = s.status(botID)
	}
	return view, nil
}

// State returns the runtime state and pairing flag without provider error text.
func (s *SetupService) State(ctx context.Context, botID string) (state string, paired bool, err error) {
	view, err := s.View(ctx, botID)
	return view.Status.State, view.Enabled, err
}

// Owner returns the current notification recipient, or empty values when unpaired.
func (s *SetupService) Owner(ctx context.Context, botID string) (account, owner string, err error) {
	row, err := s.q.GetWechatSettings(ctx, botID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return row.WechatBotID, row.Owner, err
}

// FingerprintLocked detects changes to credentials or QR login before approval.
func (s *SetupService) FingerprintLocked(ctx context.Context, botID string) ([sha256.Size]byte, error) {
	row, err := s.q.GetWechatSettings(ctx, botID)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	var id string
	var confirmed bool
	if session := s.sessions[botID]; session != nil {
		id, confirmed = session.id, session.confirmed.ID != ""
	}
	encoded, err := json.Marshal([]any{row, id, confirmed})
	return sha256.Sum256(encoded), err
}

// Begin replaces a temporary QR session. No credential is stored yet.
func (s *SetupService) Begin(ctx context.Context, botID string) (LoginView, error) {
	session := &loginSession{id: uuid.NewV7().String()}
	s.mu.Lock()
	s.sessions[botID] = session
	s.mu.Unlock()
	login, view, err := beginLogin(ctx, session.id, s.options)
	if err != nil {
		return view, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[botID] != session {
		return view, fmt.Errorf("%w: WeChat login was replaced", ErrInvalid)
	}
	if err := db.RequireActive(ctx, s.q, botID); err != nil {
		return view, err
	}
	session.login = login
	return view, nil
}

// Poll saves credentials only if the same session is still current after polling.
func (s *SetupService) Poll(ctx context.Context, botID, id, code string) (LoginView, error) {
	if code != "" && !verifyDigits.MatchString(code) {
		return LoginView{}, fmt.Errorf("%w: enter the digits shown on your phone", ErrInvalid)
	}
	s.mu.Lock()
	session := s.sessions[botID]
	if session == nil || id != session.id {
		s.mu.Unlock()
		return LoginView{}, fmt.Errorf("%w: start a new WeChat login", ErrInvalid)
	}
	if session.confirmed.ID != "" {
		view, err := session.confirmed, db.RequireActive(ctx, s.q, botID)
		s.mu.Unlock()
		return view, err
	}
	login := session.login
	s.mu.Unlock()
	if login == nil {
		return LoginView{}, fmt.Errorf("%w: start a new WeChat login", ErrInvalid)
	}

	// Take the login lock first. A remote poll must never delay the settings lock.
	login.mu.Lock()
	defer login.mu.Unlock()
	view, err := login.pollLocked(ctx, code)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[botID] != session {
		return LoginView{}, fmt.Errorf("%w: WeChat login was replaced", ErrInvalid)
	}
	if err != nil {
		return view, err
	}
	if err := db.RequireActive(ctx, s.q, botID); err != nil {
		return view, err
	}
	if login.candidate == nil {
		return view, nil
	}
	if err := s.activateLocked(ctx, botID, login.candidate); err != nil {
		return view, err
	}
	// Keep confirmation available for a retry, without retaining a live login.
	view = login.confirmLocked()
	session.login, session.confirmed = nil, view
	return view, nil
}

func (s *SetupService) activateLocked(ctx context.Context, botID string, cfg *Config) error {
	previous, err := s.Config(ctx, botID)
	if err != nil {
		return err
	}
	// Join old requests before resetting runtime state shared with the replacement.
	s.onChange(botID, nil)
	if err := db.WriteActiveChannel(ctx, s.db, botID, func(q *sqlc.Queries) error {
		owner, err := q.WechatCredentialOwner(ctx, cfg.BotID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && owner.ID != botID {
			return fmt.Errorf("%w: wechat is used by bot %q (%s)", ErrCredentialInUse, owner.Name, owner.ID)
		}
		if err := q.SetWechatSettings(ctx, sqlc.SetWechatSettingsParams{BotID: botID, Token: cfg.Token, BaseUrl: cfg.BaseURL, WechatBotID: cfg.BotID, Owner: cfg.Owner}); err != nil {
			return err
		}
		if _, err := q.EnsureWechatRuntime(ctx, sqlc.EnsureWechatRuntimeParams{BotID: cfg.BotID, Owner: cfg.Owner}); err != nil {
			return err
		}
		return q.SetWechatPause(ctx, sqlc.SetWechatPauseParams{BotID: cfg.BotID, Owner: cfg.Owner, PauseUntil: 0})
	}); err != nil {
		if previous != nil && db.CanStartChannel(ctx, s.q, botID) {
			s.onChange(botID, previous)
		}
		return err
	}
	if db.CanStartChannel(ctx, s.q, botID) {
		s.onChange(botID, cfg)
	}
	return nil
}

// Delete stops the bot, forgets credentials, and invalidates any QR poll in flight.
func (s *SetupService) Delete(ctx context.Context, botID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.DisconnectLocked(ctx, botID)
}

// DisconnectLocked removes credentials after it stops the runtime.
func (s *SetupService) DisconnectLocked(ctx context.Context, botID string) error {
	s.CancelLoginLocked(botID)
	previous, err := s.Config(ctx, botID)
	if err != nil {
		return err
	}
	s.onChange(botID, nil)
	if err := db.WriteActiveChannel(ctx, s.db, botID, func(q *sqlc.Queries) error {
		return q.DeleteWechatSettings(ctx, botID)
	}); err != nil {
		if previous != nil && db.CanStartChannel(ctx, s.q, botID) {
			s.onChange(botID, previous)
		}
		return err
	}
	return nil
}

// CancelLoginLocked invalidates responses from remote calls already in flight.
func (s *SetupService) CancelLoginLocked(botID string) {
	delete(s.sessions, botID)
}

// StopLocked cancels login and stops the runtime without changing saved settings.
func (s *SetupService) StopLocked(botID string) {
	s.CancelLoginLocked(botID)
	s.onChange(botID, nil)
}

// PrepareStartLocked loads settings before the coordinator starts any channel.
// The returned function requires the same lock and rechecks archive state before starting.
func (s *SetupService) PrepareStartLocked(ctx context.Context, botID string) (func(context.Context), error) {
	cfg, err := s.Config(ctx, botID)
	if err != nil {
		return nil, err
	}
	return func(startCtx context.Context) {
		if cfg == nil || db.CanStartChannel(startCtx, s.q, botID) {
			s.onChange(botID, cfg)
		}
	}, nil
}
