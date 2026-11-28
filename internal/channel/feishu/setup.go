package feishu

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

var (
	ErrInvalid         = errors.New("invalid settings")
	ErrCredentialInUse = errors.New("channel credential already in use")
)

// Feishu is the public view. It never contains the app secret or pairing code.
type Feishu struct {
	Status    string `json:"status"`
	Error     string `json:"error"`
	Enabled   bool   `json:"enabled"`
	AppID     string `json:"appId"`
	Brand     string `json:"brand"`
	Transport string `json:"transport"`
	Paired    bool   `json:"paired"`
	Reactions *bool  `json:"reactions,omitempty"`
}

// Setup is a short-lived app creation or pairing session.
type Setup struct {
	Session     string `json:"session"`
	QRURL       string `json:"qrUrl"`
	Interval    int    `json:"interval"`
	ExpiresIn   int    `json:"expiresIn"`
	Status      string `json:"status"`
	PairingCode string `json:"pairingCode"`
}

type registrationSession struct {
	id, device, brand string
	expires, next     time.Time
}

// SetupService owns saved settings and in-flight registrations for Feishu and Lark.
// The app shares mu with the channel coordinator. Locked methods require that lock.
type SetupService struct {
	q         *sqlc.Queries
	db        *sql.DB
	mu        *sync.Mutex
	apiURL    string
	registrar Registrar
	onChange  func(string, *Config)
	status    func(string) channel.Status
	sessions  map[string]*registrationSession
}

// NewSetup connects storage, the settings lock, and the runtime callbacks.
func NewSetup(q *sqlc.Queries, conn *sql.DB, mu *sync.Mutex, apiURL, accountsURL string, onChange func(string, *Config), status func(string) channel.Status) *SetupService {
	return &SetupService{q: q, db: conn, mu: mu, apiURL: apiURL, registrar: Registrar{AccountsURL: accountsURL}, onChange: onChange, status: status, sessions: make(map[string]*registrationSession)}
}

// Config returns the saved credentials, or nil when off.
func (s *SetupService) Config(ctx context.Context, botID string) (*Config, error) {
	row, err := s.q.GetFeishuSettings(ctx, botID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var reactions *bool
	if row.Reactions.Valid {
		reactions = new(row.Reactions.Int64 == 1)
	}
	return &Config{Reactions: reactions, AppID: row.AppID, AppSecret: row.AppSecret, Brand: row.Brand, OwnerID: row.OwnerID, APIURL: s.apiURL,
		Webhook: WebhookConfig{VerificationToken: row.VerificationToken, EncryptKey: row.EncryptKey}}, nil
}

// View returns the saved public configuration.
func (s *SetupService) View(ctx context.Context, botID string) (Feishu, error) {
	cfg, err := s.Config(ctx, botID)
	if err != nil {
		return Feishu{}, err
	}
	if cfg == nil {
		return Feishu{Brand: "feishu", Status: "off", Transport: "websocket"}, nil
	}
	runtime := s.status(botID)
	transport := "websocket"
	if cfg.Webhook.VerificationToken != "" {
		transport = "webhook"
	}
	return Feishu{Enabled: true, Reactions: cfg.Reactions, AppID: cfg.AppID, Brand: cfg.Brand, Transport: transport, Paired: cfg.OwnerID != "", Status: runtime.State, Error: runtime.Reason}, nil
}

// State returns the runtime state and pairing flag without provider error text.
func (s *SetupService) State(ctx context.Context, botID string) (state string, paired bool, err error) {
	view, err := s.View(ctx, botID)
	return view.Status, view.Paired, err
}

// Owner returns the current notification recipient, or empty values when unpaired.
func (s *SetupService) Owner(ctx context.Context, botID string) (account, owner string, err error) {
	row, err := s.q.GetFeishuSettings(ctx, botID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil || row.OwnerID == "" {
		return "", "", err
	}
	return row.AppID, row.OwnerID, nil
}

// FingerprintLocked detects changes to credentials, pairing, or a registration before approval.
func (s *SetupService) FingerprintLocked(ctx context.Context, botID string) ([sha256.Size]byte, error) {
	row, err := s.q.GetFeishuSettings(ctx, botID)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	var setup string
	if session := s.sessions[botID]; session != nil {
		setup = session.id
	}
	encoded, err := json.Marshal([]any{row, setup})
	return sha256.Sum256(encoded), err
}

func validBrand(brand string) bool { return brand == "feishu" || brand == "lark" }

// Set saves manual credentials and resets owner authorization on a credential change.
func (s *SetupService) Set(ctx context.Context, botID string, appID, secret, brand string, reactions *bool, transport string, webhook *WebhookConfig) (Setup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	appID = strings.TrimSpace(appID)
	secret = strings.TrimSpace(secret)
	if !validBrand(brand) || appID == "" {
		return Setup{}, fmt.Errorf("%w: enter an App ID and choose Feishu or Lark", ErrInvalid)
	}
	cfg := Config{Reactions: reactions, AppID: appID, AppSecret: secret, Brand: brand, APIURL: s.apiURL}
	// Pairing uses TryLock, so the snapshot stays valid while the old bot stops.
	saved, err := s.Config(ctx, botID)
	if err != nil {
		return Setup{}, err
	}
	if saved != nil && cfg.Reactions == nil {
		cfg.Reactions = saved.Reactions
	}
	if saved != nil && saved.AppID == appID && saved.Brand == brand {
		cfg.Webhook = saved.Webhook
		if cfg.AppSecret == "" {
			cfg.AppSecret = saved.AppSecret
		}
		if cfg.AppSecret == saved.AppSecret {
			cfg.OwnerID = saved.OwnerID
		}
	}
	if cfg.AppSecret == "" {
		return Setup{}, fmt.Errorf("%w: enter an App Secret", ErrInvalid)
	}
	switch {
	case transport == "websocket" && webhook == nil:
		cfg.Webhook = WebhookConfig{}
	case transport == "webhook":
		if webhook != nil {
			cfg.Webhook = *webhook
		}
		if strings.TrimSpace(cfg.Webhook.VerificationToken) == "" {
			return Setup{}, fmt.Errorf("%w: enter the Feishu webhook verification token", ErrInvalid)
		}
	case transport == "" && webhook == nil:
	default:
		return Setup{}, fmt.Errorf("%w: choose webhook transport to set Feishu webhook credentials", ErrInvalid)
	}
	var pairing string
	if cfg.OwnerID == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return Setup{}, err
		}
		pairing = hex.EncodeToString(b)
	}
	if err := s.replaceLocked(ctx, botID, saved, cfg, pairing); err != nil {
		return Setup{}, err
	}
	s.CancelRegistrationLocked(botID)
	return Setup{Status: "saved", PairingCode: pairing, ExpiresIn: 600}, nil
}

// replaceLocked joins the old runtime before a write can change its authority.
func (s *SetupService) replaceLocked(ctx context.Context, botID string, saved *Config, cfg Config, pairing string) error {
	s.onChange(botID, nil)
	if err := s.save(ctx, botID, cfg, pairing); err != nil {
		if saved != nil && db.CanStartChannel(ctx, s.q, botID) {
			s.onChange(botID, saved)
		}
		return err
	}
	if db.CanStartChannel(ctx, s.q, botID) {
		s.onChange(botID, &cfg)
	}
	return nil
}

func (s *SetupService) save(ctx context.Context, botID string, cfg Config, pairing string) error {
	var reactions sql.NullInt64
	if cfg.Reactions != nil {
		reactions.Valid = true
		if *cfg.Reactions {
			reactions.Int64 = 1
		}
	}
	return db.WriteActiveChannel(ctx, s.db, botID, func(q *sqlc.Queries) error {
		owner, err := q.FeishuCredentialOwner(ctx, cfg.AppID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && owner.ID != botID {
			return fmt.Errorf("%w: feishu is used by bot %q (%s)", ErrCredentialInUse, owner.Name, owner.ID)
		}
		return q.SetFeishuSettings(ctx, sqlc.SetFeishuSettingsParams{Reactions: reactions, BotID: botID, AppID: cfg.AppID, AppSecret: cfg.AppSecret, Brand: cfg.Brand, OwnerID: cfg.OwnerID, PairingCode: pairing, PairingExpiresAt: time.Now().Add(10 * time.Minute).UnixMilli(), VerificationToken: cfg.Webhook.VerificationToken, EncryptKey: cfg.Webhook.EncryptKey})
	})
}

// Pair consumes a one-time code only from a DM. The caller checks the DM boundary.
func (s *SetupService) Pair(ctx context.Context, botID, owner, code string) (bool, error) {
	// Never wait: settings changes hold mu while stopping this bot.
	if !s.mu.TryLock() {
		return false, errors.New("channel settings are changing; retry pairing")
	}
	defer s.mu.Unlock()
	if owner == "" || code == "" {
		return false, nil
	}
	var rows int64
	err := db.WriteActiveChannel(ctx, s.db, botID, func(q *sqlc.Queries) error {
		var err error
		rows, err = q.PairFeishuOwner(ctx, sqlc.PairFeishuOwnerParams{BotID: botID, OwnerID: owner, PairingCode: code, PairingExpiresAt: time.Now().UnixMilli()})
		return err
	})
	// The running bot adopts the saved owner without restarting itself.
	return err == nil && rows == 1, err
}

// RevokePairingLocked keeps the credentials. A new code must come from Settings.
func (s *SetupService) RevokePairingLocked(ctx context.Context, botID string) error {
	saved, err := s.Config(ctx, botID)
	if err != nil {
		return err
	}
	if saved == nil {
		return sql.ErrNoRows
	}
	cfg := *saved
	cfg.OwnerID = ""
	return s.replaceLocked(ctx, botID, saved, cfg, "")
}

// Delete removes credentials and cancels a pending registration.
func (s *SetupService) Delete(ctx context.Context, botID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.DisconnectLocked(ctx, botID)
}

// DisconnectLocked removes credentials after it stops the runtime.
func (s *SetupService) DisconnectLocked(ctx context.Context, botID string) error {
	saved, err := s.Config(ctx, botID)
	if err != nil {
		return err
	}
	s.onChange(botID, nil)
	if err := db.WriteActiveChannel(ctx, s.db, botID, func(q *sqlc.Queries) error {
		return q.DeleteFeishuSettings(ctx, botID)
	}); err != nil {
		if saved != nil && db.CanStartChannel(ctx, s.q, botID) {
			s.onChange(botID, saved)
		}
		return err
	}
	s.CancelRegistrationLocked(botID)
	return nil
}

// CancelRegistrationLocked invalidates responses from remote calls already in flight.
func (s *SetupService) CancelRegistrationLocked(botID string) {
	delete(s.sessions, botID)
}

// StopLocked cancels registration and stops the runtime without changing saved settings.
func (s *SetupService) StopLocked(botID string) {
	s.CancelRegistrationLocked(botID)
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

// Begin keeps the platform device code on the server.
func (s *SetupService) Begin(ctx context.Context, botID, brand string) (Setup, error) {
	if !validBrand(brand) {
		return Setup{}, fmt.Errorf("%w: choose Feishu or Lark", ErrInvalid)
	}
	session := &registrationSession{id: uuid.NewV7().String(), brand: brand}
	s.mu.Lock()
	s.sessions[botID] = session
	s.mu.Unlock()
	data, err := s.registrar.Begin(ctx, brand)
	if err != nil {
		return Setup{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[botID] != session {
		return Setup{}, fmt.Errorf("%w: registration was replaced or canceled", ErrInvalid)
	}
	if err := db.RequireActive(ctx, s.q, botID); err != nil {
		return Setup{}, err
	}
	now := time.Now()
	session.device = data.DeviceCode
	session.expires = now.Add(time.Duration(data.ExpiresIn) * time.Second)
	session.next = now.Add(time.Duration(data.Interval) * time.Second)
	return Setup{Session: session.id, QRURL: data.QRURL, Interval: data.Interval, ExpiresIn: data.ExpiresIn, Status: "pending"}, nil
}

// Poll saves credentials only from the active server-owned device session.
func (s *SetupService) Poll(ctx context.Context, botID, id string) (Setup, error) {
	s.mu.Lock()
	session := s.sessions[botID]
	if session == nil || id != session.id || time.Now().After(session.expires) {
		s.mu.Unlock()
		return Setup{}, fmt.Errorf("%w: registration expired; create a new QR code", ErrInvalid)
	}
	if time.Now().Before(session.next) {
		interval := max(1, int(time.Until(session.next).Seconds())+1)
		s.mu.Unlock()
		return Setup{Session: id, Status: "pending", Interval: interval}, nil
	}
	// Reserve the next poll while the remote request runs without the settings lock.
	session.next = time.Now().Add(20 * time.Second)
	s.mu.Unlock()
	data, err := s.registrar.Poll(ctx, session.brand, session.device)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[botID] != session || time.Now().After(session.expires) {
		return Setup{}, fmt.Errorf("%w: registration was replaced or expired", ErrInvalid)
	}
	if err != nil {
		return Setup{}, err
	}
	if err := db.RequireActive(ctx, s.q, botID); err != nil {
		return Setup{}, err
	}
	if data.Error != "" {
		session.next = time.Now().Add(time.Duration(data.Interval) * time.Second)
		return Setup{Session: id, Status: "pending", Interval: data.Interval}, nil
	}
	brand := session.brand
	if data.UserInfo.Brand == "lark" {
		brand = "lark"
	}
	cfg := Config{AppID: data.AppID, AppSecret: data.AppSecret, OwnerID: data.UserInfo.OpenID, Brand: brand, APIURL: s.apiURL}
	saved, err := s.Config(ctx, botID)
	if err != nil {
		return Setup{}, err
	}
	if saved != nil {
		cfg.Reactions = saved.Reactions
	}
	if err := s.replaceLocked(ctx, botID, saved, cfg, ""); err != nil {
		return Setup{}, err
	}
	s.CancelRegistrationLocked(botID)
	return Setup{Status: "created"}, nil
}
