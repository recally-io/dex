package settings

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

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/feishu"
)

// Feishu is the public view. It never contains the app secret or pairing code.
type Feishu struct {
	Status  string `json:"status"`
	Error   string `json:"error"`
	Enabled bool   `json:"enabled"`
	AppID   string `json:"appId"`
	Brand   string `json:"brand"`
	Paired  bool   `json:"paired"`
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
func (s *Service) ConfigureFeishu(apiURL, accountsURL string, onChange func(*feishu.Config)) {
	s.feishuAPIURL = apiURL
	s.feishuRegistrar = feishu.Registrar{AccountsURL: accountsURL}
	s.onFeishu = onChange
}

// FeishuConfig returns the saved credentials, or nil when off.
func (s *Service) FeishuConfig(ctx context.Context) (*feishu.Config, error) {
	row, err := s.q.GetFeishuSettings(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &feishu.Config{AppID: row.AppID, AppSecret: row.AppSecret, Brand: row.Brand, OwnerID: row.OwnerID, APIURL: s.feishuAPIURL}, nil
}

// GetFeishu returns the saved public configuration.
func (s *Service) GetFeishu(ctx context.Context) (Feishu, error) {
	cfg, err := s.FeishuConfig(ctx)
	if err != nil {
		return Feishu{}, err
	}
	if cfg == nil {
		return Feishu{Brand: "feishu", Status: "off"}, nil
	}
	status := "running"
	if cfg.OwnerID == "" {
		status = "needs_login"
	}
	reason := ""
	if s.ChannelStatus != nil {
		runtime := s.ChannelStatus("feishu")
		status, reason = runtime.State, runtime.Reason
	}
	return Feishu{Enabled: true, AppID: cfg.AppID, Brand: cfg.Brand, Paired: cfg.OwnerID != "", Status: status, Error: reason}, nil
}

func validBrand(brand string) bool { return brand == "feishu" || brand == "lark" }

// SetFeishu saves manual credentials and resets owner authorization on a credential change.
func (s *Service) SetFeishu(ctx context.Context, appID, secret, brand string) (FeishuSetup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	appID = strings.TrimSpace(appID)
	secret = strings.TrimSpace(secret)
	if !validBrand(brand) || appID == "" {
		return FeishuSetup{}, fmt.Errorf("%w: enter an App ID and choose Feishu or Lark", ErrInvalid)
	}
	cfg := feishu.Config{AppID: appID, AppSecret: secret, Brand: brand, APIURL: s.feishuAPIURL}
	saved, err := s.FeishuConfig(ctx)
	if err != nil {
		return FeishuSetup{}, err
	}
	if saved != nil && saved.AppID == appID && saved.Brand == brand {
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
	var pairing string
	if cfg.OwnerID == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return FeishuSetup{}, err
		}
		pairing = hex.EncodeToString(b)
	}
	if err := s.saveFeishu(ctx, cfg, pairing); err != nil {
		return FeishuSetup{}, err
	}
	s.feishuSession = nil
	return FeishuSetup{Status: "saved", PairingCode: pairing, ExpiresIn: 600}, nil
}

func (s *Service) saveFeishu(ctx context.Context, cfg feishu.Config, pairing string) error {
	if err := s.q.SetFeishuSettings(ctx, sqlc.SetFeishuSettingsParams{AppID: cfg.AppID, AppSecret: cfg.AppSecret, Brand: cfg.Brand, OwnerID: cfg.OwnerID, PairingCode: pairing, PairingExpiresAt: time.Now().Add(10 * time.Minute).UnixMilli()}); err != nil {
		return err
	}
	if s.onFeishu != nil {
		s.onFeishu(&cfg)
	}
	return nil
}

// PairFeishu consumes a one-time code only from a DM. The caller checks the DM boundary.
func (s *Service) PairFeishu(ctx context.Context, owner, code string) (bool, error) {
	if owner == "" || code == "" {
		return false, nil
	}
	rows, err := s.q.PairFeishuOwner(ctx, sqlc.PairFeishuOwnerParams{OwnerID: owner, PairingCode: code, PairingExpiresAt: time.Now().UnixMilli()})
	// The running bot adopts the saved owner without restarting itself.
	return rows == 1, err
}

// DeleteFeishu removes credentials and cancels a pending registration.
func (s *Service) DeleteFeishu(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.q.DeleteFeishuSettings(ctx); err != nil {
		return err
	}
	s.feishuSession = nil
	if s.onFeishu != nil {
		s.onFeishu(nil)
	}
	return nil
}

// BeginFeishu keeps the platform device code on the server.
func (s *Service) BeginFeishu(ctx context.Context, brand string) (FeishuSetup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validBrand(brand) {
		return FeishuSetup{}, fmt.Errorf("%w: choose Feishu or Lark", ErrInvalid)
	}
	data, err := s.feishuRegistrar.Begin(ctx, brand)
	if err != nil {
		return FeishuSetup{}, err
	}
	id := uuid.NewV7().String()
	s.feishuSession = &feishuSession{device: data.DeviceCode, brand: brand, expires: time.Now().Add(time.Duration(data.ExpiresIn) * time.Second), next: time.Now().Add(time.Duration(data.Interval) * time.Second)}
	s.feishuSessionID = id
	return FeishuSetup{Session: id, QRURL: data.QRURL, Interval: data.Interval, ExpiresIn: data.ExpiresIn, Status: "pending"}, nil
}

// PollFeishu saves credentials only from the active server-owned device session.
func (s *Service) PollFeishu(ctx context.Context, id string) (FeishuSetup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.feishuSession
	if session == nil || id != s.feishuSessionID || time.Now().After(session.expires) {
		return FeishuSetup{}, fmt.Errorf("%w: registration expired; create a new QR code", ErrInvalid)
	}
	if time.Now().Before(session.next) {
		return FeishuSetup{Session: id, Status: "pending", Interval: max(1, int(time.Until(session.next).Seconds())+1)}, nil
	}
	data, err := s.feishuRegistrar.Poll(ctx, session.brand, session.device)
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
	if err := s.saveFeishu(ctx, cfg, ""); err != nil {
		return FeishuSetup{}, err
	}
	s.feishuSession = nil
	return FeishuSetup{Status: "created"}, nil
}
