package telegram

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/vault"
)

var (
	ErrInvalid         = errors.New("invalid settings")
	ErrCredentialInUse = errors.New("channel credential already in use")
)

// SetupService owns credentials, pairing, and managed setup for Telegram.
// The app shares mu with the channel coordinator. Locked methods require that lock.
type SetupService struct {
	q        *sqlc.Queries
	db       *sql.DB
	mu       *sync.Mutex
	apiURL   string
	vault    *vault.Store
	manager  *telegramManager
	onChange func(string, *Config)
	status   func(string) channel.Status
}

// NewSetup connects storage, the settings lock, and runtime callbacks.
func NewSetup(q *sqlc.Queries, conn *sql.DB, mu *sync.Mutex, apiURL string, store *vault.Store, onChange func(string, *Config), status func(string) channel.Status) *SetupService {
	return &SetupService{q: q, db: conn, mu: mu, apiURL: apiURL, vault: store, onChange: onChange, status: status}
}

// Telegram describes the bot without its token.
type Telegram struct {
	Status                   channel.Status `json:"status"`
	Enabled                  bool           `json:"enabled"`
	BotID                    string         `json:"botId,omitempty"`
	AllowedUsers             []int64        `json:"allowedUsers"`
	Options                  Options        `json:"options"`
	ManagerConfigured        bool           `json:"managerConfigured,omitzero"`
	ManagedCreationAvailable bool           `json:"managedCreationAvailable,omitzero"`
	Transport                string         `json:"transport"`
	WebhookURL               string         `json:"webhookUrl,omitempty"`
}

// View returns public Telegram settings and managed setup availability.
func (s *SetupService) View(ctx context.Context, botID string) (Telegram, error) {
	view, err := s.view(ctx, botID)
	if err == nil {
		view.ManagerConfigured, view.ManagedCreationAvailable = s.managerStatus()
	}
	return view, err
}

// view leaves out manager status so callers can hold the settings lock.
func (s *SetupService) view(ctx context.Context, botID string) (Telegram, error) {
	view := Telegram{AllowedUsers: []int64{}, Status: channel.Status{State: "off"}, Transport: "polling"}
	row, err := s.q.GetTelegramSettings(ctx, botID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Telegram{}, err
	}
	if err == nil {
		view.Enabled = true
		view.BotID, _, _ = strings.Cut(row.Token, ":")
		view.AllowedUsers, err = parseUsers(row.AllowedUsers)
		if err != nil {
			return Telegram{}, err
		}
		if err := json.Unmarshal([]byte(row.Options), &view.Options); err != nil {
			return Telegram{}, err
		}
		if row.WebhookUrl != "" {
			view.Transport, view.WebhookURL = "webhook", row.WebhookUrl
		}
	}
	if s.status != nil {
		view.Status = s.status(botID)
	}
	return view, nil
}

// State returns the runtime state and pairing flag without provider error text.
func (s *SetupService) State(ctx context.Context, botID string) (state string, paired bool, err error) {
	view, err := s.view(ctx, botID)
	return view.Status.State, len(view.AllowedUsers) != 0, err
}

// NotificationPeers returns the authorized destinations for the saved account.
func (s *SetupService) NotificationPeers(ctx context.Context, botID string) (string, []string, error) {
	row, err := s.q.GetTelegramSettings(ctx, botID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	account, _, ok := strings.Cut(row.Token, ":")
	if _, err := strconv.ParseUint(account, 10, 64); !ok || err != nil {
		return "", nil, fmt.Errorf("%w: invalid Telegram bot identity", ErrInvalid)
	}
	users, err := parseUsers(row.AllowedUsers)
	if err != nil {
		return "", nil, err
	}
	peers := make([]string, len(users))
	for i, user := range users {
		peers[i] = "telegram:" + strconv.FormatInt(user, 10)
	}
	return account, peers, nil
}

// FingerprintLocked detects changes to the saved account or pairing before approval.
func (s *SetupService) FingerprintLocked(ctx context.Context, botID string) ([sha256.Size]byte, error) {
	row, err := s.q.GetTelegramSettings(ctx, botID)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	row.RetryAt = 0 // Delivery backoff does not change pairing authority.
	encoded, err := json.Marshal(row)
	return sha256.Sum256(encoded), err
}

// Config returns the saved bot configuration, or nil when Telegram is off.
func (s *SetupService) Config(ctx context.Context, botID string) (*Config, error) {
	row, err := s.q.GetTelegramSettings(ctx, botID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	users, err := parseUsers(row.AllowedUsers)
	if err != nil {
		return nil, err
	}
	var options Options
	if err := json.Unmarshal([]byte(row.Options), &options); err != nil {
		return nil, err
	}
	cfg := &Config{Options: options, Token: row.Token, AllowedUsers: users, APIURL: s.apiURL, Pair: s.pairTelegram(botID, row.Token), RetryAt: row.RetryAt,
		Webhook: WebhookConfig{URL: row.WebhookUrl, SecretToken: row.WebhookSecret}}
	if strings.Contains(row.Token, telegramVaultMarker) {
		cfg.CredentialRef = row.Token
		cfg.Token, err = s.vault.Credential(row.Token)
		if err != nil {
			return nil, err
		}
		if cfg.Token == "" {
			return nil, errors.New("the saved managed Telegram credential is missing")
		}
	}
	return cfg, nil
}

// Set saves the configuration and restarts the bot. An empty token keeps the saved token.
func (s *SetupService) Set(ctx context.Context, botID string, token string, allowedUsers []int64, options *Options, transport string, webhook *WebhookConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setLocked(ctx, botID, token, allowedUsers, options, transport, webhook)
}

func (s *SetupService) setLocked(ctx context.Context, botID, token string, allowedUsers []int64, options *Options, transport string, webhook *WebhookConfig) error {
	if err := db.RequireActive(ctx, s.q, botID); err != nil {
		return err
	}
	// Pairing uses TryLock, so this snapshot cannot change before the old bot joins.
	saved, err := s.Config(ctx, botID)
	if err != nil {
		return err
	}
	if saved != nil {
		token = cmp.Or(token, saved.Token)
	}
	cfg := Config{Token: strings.TrimSpace(token), AllowedUsers: allowedUsers, APIURL: s.apiURL}
	if saved != nil {
		cfg.Options = saved.Options
		if cfg.Token == saved.Token {
			cfg.CredentialRef = saved.CredentialRef
		}
	}
	if options != nil {
		cfg.Options = *options
	}
	if strings.Contains(cfg.Token, telegramVaultMarker) {
		return fmt.Errorf("%w: enter a bot token from BotFather", ErrInvalid)
	}
	if s.manager != nil && strings.HasPrefix(cfg.Token, strconv.FormatInt(s.manager.id, 10)+":") {
		return fmt.Errorf("%w: the manager bot must be separate from the channel bot", ErrInvalid)
	}
	key := cmp.Or(cfg.CredentialRef, cfg.Token)
	cfg.Pair = s.pairTelegram(botID, key)
	account, _, _ := strings.Cut(cfg.Token, ":")
	sameBot := saved != nil && strings.HasPrefix(saved.Token, account+":")
	if sameBot {
		cfg.Webhook = saved.Webhook
	}
	switch {
	case transport == "polling" && webhook == nil:
		cfg.Webhook = WebhookConfig{}
	case transport == "webhook":
		if webhook != nil {
			cfg.Webhook = *webhook
		}
		if cfg.Webhook.URL == "" {
			return fmt.Errorf("%w: enter the Telegram webhook URL and secret", ErrInvalid)
		}
	case transport == "" && webhook == nil:
	default:
		return fmt.Errorf("%w: choose webhook transport to set Telegram webhook credentials", ErrInvalid)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := s.checkTelegramAccount(ctx, botID, account); err != nil {
		return err
	}
	users := make([]string, len(allowedUsers))
	for i, id := range allowedUsers {
		users[i] = strconv.FormatInt(id, 10)
	}
	encoded, err := json.Marshal(cfg.Options)
	if err != nil {
		return err
	}
	s.onChange(botID, nil)
	committed := false
	defer func() {
		if !committed && saved != nil && db.CanStartChannel(ctx, s.q, botID) {
			s.onChange(botID, saved)
		}
	}()
	if saved != nil && saved.Webhook.URL != "" && (!sameBot || cfg.Webhook.URL == "") {
		teardown := *saved
		if sameBot {
			teardown.Token = cfg.Token
		}
		if err := DeleteWebhook(ctx, teardown); err != nil {
			return err
		}
	}
	if err := db.WriteActiveChannel(ctx, s.db, botID, func(q *sqlc.Queries) error {
		var err error
		cfg.RetryAt, err = q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{BotID: botID,
			Token: key, AllowedUsers: strings.Join(users, ","), Options: string(encoded),
			WebhookUrl: cfg.Webhook.URL, WebhookSecret: cfg.Webhook.SecretToken,
		})
		return err
	}); err != nil {
		return err
	}
	committed = true
	if db.CanStartChannel(ctx, s.q, botID) {
		s.onChange(botID, &cfg)
	}
	if saved != nil && saved.CredentialRef != cfg.CredentialRef {
		s.forgetTelegramCredential(saved.CredentialRef)
	}
	return nil
}

// RevokePairingLocked clears the owners and code without changing the account or transport.
func (s *SetupService) RevokePairingLocked(ctx context.Context, botID string) error {
	return s.setLocked(ctx, botID, "", nil, nil, "", nil)
}

// Delete turns Telegram off and forgets the token.
func (s *SetupService) Delete(ctx context.Context, botID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.DisconnectLocked(ctx, botID)
}

// DisconnectLocked stops the runtime, revokes the webhook, and deletes saved settings.
func (s *SetupService) DisconnectLocked(ctx context.Context, botID string) error {
	saved, err := s.Config(ctx, botID)
	if err != nil {
		return err
	}
	s.onChange(botID, nil)
	committed := false
	defer func() {
		if !committed && saved != nil && db.CanStartChannel(ctx, s.q, botID) {
			s.onChange(botID, saved)
		}
	}()
	if saved != nil && saved.Webhook.URL != "" {
		if err := DeleteWebhook(ctx, *saved); err != nil {
			return err
		}
	}
	if err := db.WriteActiveChannel(ctx, s.db, botID, func(q *sqlc.Queries) error {
		return q.DeleteTelegramSettings(ctx, botID)
	}); err != nil {
		return err
	}
	committed = true
	// Run cleanup again for staged uploads that deletion discarded.
	s.onChange(botID, nil)
	if saved != nil {
		s.forgetTelegramCredential(saved.CredentialRef)
	}
	return nil
}

// StopLocked stops the runtime without changing saved settings.
func (s *SetupService) StopLocked(botID string) {
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

func parseUsers(s string) ([]int64, error) {
	users := []int64{}
	if s == "" {
		return users, nil
	}
	for field := range strings.SplitSeq(s, ",") {
		id, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("telegram_settings.allowed_users: %w", err)
		}
		users = append(users, id)
	}
	return users, nil
}
