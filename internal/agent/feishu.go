package agent

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/channel/feishu"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
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

// FeishuSetup is a short-lived app creation or pairing session.
type FeishuSetup struct {
	Session     string `json:"session"`
	QRURL       string `json:"qrUrl"`
	Interval    int    `json:"interval"`
	ExpiresIn   int    `json:"expiresIn"`
	Status      string `json:"status"`
	PairingCode string `json:"pairingCode"`
}

type feishuSession struct {
	device, brand string
	expires, next time.Time
}

// ConfigureFeishu connects the runtime and registration test boundary before Start.
func (s *Service) ConfigureFeishu(apiURL, accountsURL string, onChange func(string, *feishu.Config)) {
	s.feishuAPIURL = apiURL
	s.feishuRegistrar = feishu.Registrar{AccountsURL: accountsURL}
	s.onFeishu = onChange
}

// FeishuConfig returns the saved credentials, or nil when off.
func (s *Service) FeishuConfig(ctx context.Context, agentID string) (*feishu.Config, error) {
	row, err := s.q.GetFeishuSettings(ctx, agentID)
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
	return &feishu.Config{Reactions: reactions, AppID: row.AppID, AppSecret: row.AppSecret, Brand: row.Brand, OwnerID: row.OwnerID, APIURL: s.feishuAPIURL,
		Webhook: feishu.WebhookConfig{VerificationToken: row.VerificationToken, EncryptKey: row.EncryptKey}}, nil
}

// GetFeishu returns the saved public configuration.
func (s *Service) GetFeishu(ctx context.Context, agentID string) (Feishu, error) {
	cfg, err := s.FeishuConfig(ctx, agentID)
	if err != nil {
		return Feishu{}, err
	}
	if cfg == nil {
		return Feishu{Brand: "feishu", Status: "off", Transport: "websocket"}, nil
	}
	runtime := s.ChannelStatus(agentID, "feishu")
	transport := "websocket"
	if cfg.Webhook.VerificationToken != "" {
		transport = "webhook"
	}
	return Feishu{Enabled: true, Reactions: cfg.Reactions, AppID: cfg.AppID, Brand: cfg.Brand, Transport: transport, Paired: cfg.OwnerID != "", Status: runtime.State, Error: runtime.Reason}, nil
}

func validBrand(brand string) bool { return brand == "feishu" || brand == "lark" }

// SetFeishu saves manual credentials and resets owner authorization on a credential change.
func (s *Service) SetFeishu(ctx context.Context, agentID string, appID, secret, brand string, reactions *bool, transport string, webhook *feishu.WebhookConfig) (FeishuSetup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	appID = strings.TrimSpace(appID)
	secret = strings.TrimSpace(secret)
	if !validBrand(brand) || appID == "" {
		return FeishuSetup{}, fmt.Errorf("%w: enter an App ID and choose Feishu or Lark", ErrInvalid)
	}
	cfg := feishu.Config{Reactions: reactions, AppID: appID, AppSecret: secret, Brand: brand, APIURL: s.feishuAPIURL}
	// Pairing uses TryLock, so the snapshot remains valid while the old bot joins.
	saved, err := s.FeishuConfig(ctx, agentID)
	if err != nil {
		return FeishuSetup{}, err
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
		return FeishuSetup{}, fmt.Errorf("%w: enter an App Secret", ErrInvalid)
	}
	switch {
	case transport == "websocket" && webhook == nil:
		cfg.Webhook = feishu.WebhookConfig{}
	case transport == "webhook":
		if webhook != nil {
			cfg.Webhook = *webhook
		}
		if strings.TrimSpace(cfg.Webhook.VerificationToken) == "" {
			return FeishuSetup{}, fmt.Errorf("%w: enter the Feishu webhook verification token", ErrInvalid)
		}
	case transport == "" && webhook == nil:
	default:
		return FeishuSetup{}, fmt.Errorf("%w: choose webhook transport to set Feishu webhook credentials", ErrInvalid)
	}
	var pairing string
	if cfg.OwnerID == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return FeishuSetup{}, err
		}
		pairing = hex.EncodeToString(b)
	}
	s.onFeishu(agentID, nil)
	savedNew := false
	defer func() {
		if !savedNew && saved != nil {
			s.onFeishu(agentID, saved)
		}
	}()
	if err := s.saveFeishu(ctx, agentID, cfg, pairing, false); err != nil {
		return FeishuSetup{}, err
	}
	savedNew = true
	s.channelSession(agentID).feishuSession = nil
	s.channelSession(agentID).feishuSessionID = ""
	return FeishuSetup{Status: "saved", PairingCode: pairing, ExpiresIn: 600}, nil
}

func (s *Service) saveFeishu(ctx context.Context, agentID string, cfg feishu.Config, pairing string, newBinding bool) error {
	// A registration poll started before an archive can reach here; an archived bot starts no channel.
	if err := db.RequireActive(ctx, s.q, agentID); err != nil {
		return err
	}
	if err := s.checkCredential(ctx, agentID, "feishu", cfg.AppID); err != nil {
		return err
	}
	var reactions sql.NullInt64
	if cfg.Reactions != nil {
		reactions.Valid = true
		if *cfg.Reactions {
			reactions.Int64 = 1
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	if newBinding {
		if err := q.RevokeChannelBindings(ctx, sqlc.RevokeChannelBindingsParams{AgentID: agentID, Platform: "feishu"}); err != nil {
			return err
		}
	}
	if err := q.SetFeishuSettings(ctx, sqlc.SetFeishuSettingsParams{Reactions: reactions, AgentID: agentID, AppID: cfg.AppID, AppSecret: cfg.AppSecret, Brand: cfg.Brand, OwnerID: cfg.OwnerID, PairingCode: pairing, PairingExpiresAt: time.Now().Add(10 * time.Minute).UnixMilli(), VerificationToken: cfg.Webhook.VerificationToken, EncryptKey: cfg.Webhook.EncryptKey}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.onFeishu(agentID, &cfg)
	return nil
}

// PairFeishu consumes a one-time code only from a DM. The caller checks the DM boundary.
func (s *Service) PairFeishu(ctx context.Context, agentID string, owner, code string) (bool, error) {
	// Never wait: settings changes hold mu while joining this bot.
	if !s.mu.TryLock() {
		return false, errors.New("channel settings are changing; retry pairing")
	}
	defer s.mu.Unlock()
	if owner == "" || code == "" {
		return false, nil
	}
	rows, err := s.q.PairFeishuOwner(ctx, sqlc.PairFeishuOwnerParams{AgentID: agentID, OwnerID: owner, PairingCode: code, PairingExpiresAt: time.Now().UnixMilli()})
	// The running bot adopts the saved owner without restarting itself.
	return rows == 1, err
}

// revokeFeishuPairing keeps the app credentials. A new code must come from Settings.
func (s *Service) revokeFeishuPairing(ctx context.Context, agentID string) error {
	saved, err := s.FeishuConfig(ctx, agentID)
	if err != nil {
		return err
	}
	if saved == nil {
		return ErrChannelApproval
	}
	cfg := *saved
	cfg.OwnerID = ""
	s.onFeishu(agentID, nil)
	if err := s.saveFeishu(ctx, agentID, cfg, "", false); err != nil {
		s.onFeishu(agentID, saved)
		return err
	}
	return nil
}

// DeleteFeishu removes credentials and cancels a pending registration.
func (s *Service) DeleteFeishu(ctx context.Context, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteFeishu(ctx, agentID)
}

func (s *Service) deleteFeishu(ctx context.Context, agentID string) error {
	saved, err := s.FeishuConfig(ctx, agentID)
	if err != nil {
		return err
	}
	s.onFeishu(agentID, nil)
	if err := s.q.DeleteFeishuSettings(ctx, agentID); err != nil {
		if saved != nil {
			s.onFeishu(agentID, saved)
		}
		return err
	}
	s.channelSession(agentID).feishuSession = nil
	s.channelSession(agentID).feishuSessionID = ""
	return nil
}

// BeginFeishu keeps the platform device code on the server.
func (s *Service) BeginFeishu(ctx context.Context, agentID string, brand string) (FeishuSetup, error) {
	if !validBrand(brand) {
		return FeishuSetup{}, fmt.Errorf("%w: choose Feishu or Lark", ErrInvalid)
	}
	id := uuid.NewV7().String()
	s.mu.Lock()
	s.channelSession(agentID).feishuSession = nil
	s.channelSession(agentID).feishuSessionID = id
	s.mu.Unlock()
	data, err := s.feishuRegistrar.Begin(ctx, brand)
	if err != nil {
		return FeishuSetup{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channelSession(agentID).feishuSessionID != id {
		return FeishuSetup{}, fmt.Errorf("%w: registration was replaced or canceled", ErrInvalid)
	}
	s.channelSession(agentID).feishuSession = &feishuSession{device: data.DeviceCode, brand: brand, expires: time.Now().Add(time.Duration(data.ExpiresIn) * time.Second), next: time.Now().Add(time.Duration(data.Interval) * time.Second)}
	return FeishuSetup{Session: id, QRURL: data.QRURL, Interval: data.Interval, ExpiresIn: data.ExpiresIn, Status: "pending"}, nil
}

// PollFeishu saves credentials only from the active server-owned device session.
func (s *Service) PollFeishu(ctx context.Context, agentID string, id string) (FeishuSetup, error) {
	s.mu.Lock()
	session := s.channelSession(agentID).feishuSession
	if session == nil || id != s.channelSession(agentID).feishuSessionID || time.Now().After(session.expires) {
		s.mu.Unlock()
		return FeishuSetup{}, fmt.Errorf("%w: registration expired; create a new QR code", ErrInvalid)
	}
	if time.Now().Before(session.next) {
		interval := max(1, int(time.Until(session.next).Seconds())+1)
		s.mu.Unlock()
		return FeishuSetup{Session: id, Status: "pending", Interval: interval}, nil
	}
	// Reserve the next poll while the remote request runs without the settings lock.
	session.next = time.Now().Add(20 * time.Second)
	s.mu.Unlock()
	data, err := s.feishuRegistrar.Poll(ctx, session.brand, session.device)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channelSession(agentID).feishuSession != session || id != s.channelSession(agentID).feishuSessionID || time.Now().After(session.expires) {
		return FeishuSetup{}, fmt.Errorf("%w: registration was replaced or expired", ErrInvalid)
	}
	if err != nil {
		return FeishuSetup{}, err
	}
	if data.Error != "" {
		session.next = time.Now().Add(time.Duration(data.Interval) * time.Second)
		return FeishuSetup{Session: id, Status: "pending", Interval: data.Interval}, nil
	}
	brand := session.brand
	if data.UserInfo.Brand == "lark" {
		brand = "lark"
	}
	cfg := feishu.Config{AppID: data.AppID, AppSecret: data.AppSecret, OwnerID: data.UserInfo.OpenID, Brand: brand, APIURL: s.feishuAPIURL}
	saved, err := s.FeishuConfig(ctx, agentID)
	if err != nil {
		return FeishuSetup{}, err
	}
	if saved != nil {
		cfg.Reactions = saved.Reactions
	}
	s.onFeishu(agentID, nil)
	if err := s.saveFeishu(ctx, agentID, cfg, "", true); err != nil {
		if saved != nil {
			s.onFeishu(agentID, saved)
		}
		return FeishuSetup{}, err
	}
	s.channelSession(agentID).feishuSession = nil
	s.channelSession(agentID).feishuSessionID = ""
	return FeishuSetup{Status: "created"}, nil
}
