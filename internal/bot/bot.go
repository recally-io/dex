// Package bot owns bot settings, activity, and channel configuration.
package bot

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/channel/feishu"
	"github.com/recally-io/dex/internal/channel/telegram"
	"github.com/recally-io/dex/internal/channel/wechat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/vault"
)

// ErrInvalid means that a requested bot or channel change is invalid.
var ErrInvalid = errors.New("invalid settings")

// ErrRunnerBusy is a change of the bot's environment while sessions that follow it have
// queued input or running background commands.
var ErrRunnerBusy = errors.New("sessions that use the environment of the bot have queued input or running background commands")

// ErrNotFound means that the bot does not exist.
var ErrNotFound = errors.New("bot not found")

var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// Service owns bot writes and channel setup sessions.
type Service struct {
	q               *sqlc.Queries
	checkModel      func(context.Context, string, string) error
	wechatOptions   wechat.Options
	onWechat        func(string, *wechat.Config)
	db              *sql.DB
	ChannelStatus   func(string, string) channel.Status
	OnRunnerChanged func(ctx context.Context, botID, oldEnvID, oldSandboxID string)
	apiURL          string
	onTelegram      func(string, *telegram.Config)
	telegramVault   *vault.Store
	manager         *telegramManager
	feishuAPIURL    string
	feishuRegistrar feishu.Registrar
	onFeishu        func(string, *feishu.Config)
	// One lock orders credential checks, saves, runtime changes, and setup sessions
	// across bots. Model checks take the settings lock; settings never takes this lock.
	mu               sync.Mutex
	sessions         map[string]*channelSession
	channelApprovals map[string]channelApproval
}

// New connects model validation and Telegram runtime changes.
func New(q *sqlc.Queries, checkModel func(context.Context, string, string) error, telegramAPIURL string, onTelegram func(string, *telegram.Config)) *Service {
	return &Service{q: q, checkModel: checkModel, apiURL: telegramAPIURL, onTelegram: onTelegram}
}

// Activity summarizes one bot for the bot list.
type Activity struct {
	AutomationFailed   bool  `json:"automationFailed"`
	ActiveSessions     int64 `json:"activeSessions"`
	RunningChannels    int64 `json:"runningChannels"`
	EnabledAutomations int64 `json:"enabledAutomations"`
}

// Bot holds the settings of one bot. An empty ModelID means no model is chosen.
type Bot struct {
	Activity         *Activity `json:"activity,omitzero"`
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Provider         string    `json:"provider"`
	ModelID          string    `json:"modelId"`
	ThinkingLevel    string    `json:"thinkingLevel"`
	FallbackProvider string    `json:"fallbackProvider"`
	FallbackModelID  string    `json:"fallbackModelId"`
	Instructions     string    `json:"instructions"`
	RunnerID         string    `json:"runnerId"`
	// ArchivedAt is the archive time in Unix milliseconds; zero for an active bot.
	ArchivedAt int64 `json:"archivedAt,omitzero"`
}

// Telegram describes the bot without its token.
type Telegram struct {
	Status                   channel.Status   `json:"status"`
	Enabled                  bool             `json:"enabled"`
	BotID                    string           `json:"botId,omitempty"`
	AllowedUsers             []int64          `json:"allowedUsers"`
	Options                  telegram.Options `json:"options"`
	ManagerConfigured        bool             `json:"managerConfigured,omitzero"`
	ManagedCreationAvailable bool             `json:"managedCreationAvailable,omitzero"`
	Transport                string           `json:"transport"`
	WebhookURL               string           `json:"webhookUrl,omitempty"`
}

// Bot reads one bot.
func (s *Service) Bot(ctx context.Context, id string) (Bot, error) {
	row, err := s.q.GetBot(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Bot{}, ErrNotFound
	}
	return botView(row), err
}

func botView(row sqlc.Bot) Bot {
	return Bot{
		ID: row.ID, Name: row.Name, Provider: row.ModelProvider, ModelID: row.ModelID,
		ThinkingLevel: row.ThinkingLevel, FallbackProvider: row.FallbackProvider, FallbackModelID: row.FallbackModelID,
		Instructions: row.Instructions, RunnerID: row.RunnerID, ArchivedAt: row.ArchivedAt.Int64,
	}
}

// ListBots returns bots and their activity in creation order.
func (s *Service) ListBots(ctx context.Context) ([]Bot, error) {
	rows, err := s.q.ListBotActivity(ctx)
	if err != nil {
		return nil, err
	}
	bots := make([]Bot, len(rows))
	for i, row := range rows {
		bots[i] = botView(row.Bot)
		current := Activity{AutomationFailed: row.AutomationFailed == 1, ActiveSessions: row.ActiveSessions, EnabledAutomations: row.EnabledAutomations}
		if s.ChannelStatus != nil {
			for _, platform := range []string{"telegram", "wechat", "feishu"} {
				if s.ChannelStatus(row.Bot.ID, platform).State == "running" {
					current.RunningChannels++
				}
			}
		}
		bots[i].Activity = &current
	}
	return bots, nil
}

// SaveBot changes one bot. Its next message uses the settings.
func (s *Service) SaveBot(ctx context.Context, id string, a Bot) error {
	var oldRow sqlc.Bot
	err := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		var getErr error
		oldRow, getErr = s.q.GetBot(ctx, id)
		if errors.Is(getErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		if getErr != nil {
			return getErr
		}
		var cleanErr error
		a, cleanErr = s.cleanBot(ctx, a)
		if cleanErr != nil {
			return cleanErr
		}
		if err := s.saveSettings(ctx, oldRow, sqlc.UpdateBotSettingsParams{
			ModelProvider: a.Provider, ModelID: a.ModelID, ThinkingLevel: a.ThinkingLevel,
			FallbackProvider: a.FallbackProvider, FallbackModelID: a.FallbackModelID,
			Instructions: a.Instructions, RunnerID: a.RunnerID, ID: id, Name: a.Name,
		}); err != nil {
			return err
		}
		return nil
	}()
	if err != nil {
		return err
	}
	if oldRow.RunnerID != a.RunnerID && s.OnRunnerChanged != nil {
		s.OnRunnerChanged(ctx, id, oldRow.RunnerID, oldRow.RunnerSandboxID)
	}
	return nil
}

// CreateBot stores a bot. Fields the patch omits come from the default bot.
func (s *Service) CreateBot(ctx context.Context, p Patch) (Bot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.withDefaults(ctx, p)
	if err != nil {
		return Bot{}, err
	}
	a := Bot{ThinkingLevel: "off"}
	applyPatch(&a, p)
	if a, err = s.cleanBot(ctx, a); err != nil {
		return Bot{}, err
	}
	row, err := s.q.CreateBot(ctx, sqlc.CreateBotParams{
		ID: uuid.NewV7().String(), Name: a.Name, ModelProvider: a.Provider, ModelID: a.ModelID,
		ThinkingLevel: a.ThinkingLevel, FallbackProvider: a.FallbackProvider, FallbackModelID: a.FallbackModelID,
		Instructions: a.Instructions, RunnerID: a.RunnerID, CreatedAt: time.Now().UnixMilli(),
	})
	return botView(row), err
}

// withDefaults fills the fields that p omits from the built-in default bot, so a new bot starts
// with its model, thinking level, fallback model, and execution environment. A saved model that
// is no longer available is not copied.
func (s *Service) withDefaults(ctx context.Context, p Patch) (Patch, error) {
	row, err := s.q.GetBot(ctx, "default")
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return Patch{}, err
	}
	d := botView(row)
	usable := func(provider, id string) (bool, error) {
		if id == "" {
			return false, nil
		}
		err := s.checkModel(ctx, provider, id)
		if errors.Is(err, engine.ErrModelsPending) {
			return false, err
		}
		return err == nil, nil
	}
	if p.Provider == nil && p.ModelID == nil {
		ok, err := usable(d.Provider, d.ModelID)
		if err != nil {
			return Patch{}, err
		}
		if ok {
			p.Provider, p.ModelID = &d.Provider, &d.ModelID
		}
	}
	if p.FallbackProvider == nil && p.FallbackModelID == nil && p.ModelID != nil && *p.ModelID != "" {
		ok, err := usable(d.FallbackProvider, d.FallbackModelID)
		if err != nil {
			return Patch{}, err
		}
		if ok {
			p.FallbackProvider, p.FallbackModelID = &d.FallbackProvider, &d.FallbackModelID
		}
	}
	if p.ThinkingLevel == nil {
		p.ThinkingLevel = &d.ThinkingLevel
	}
	if p.RunnerID == nil {
		p.RunnerID = &d.RunnerID
	}
	return p, nil
}

func (s *Service) cleanBot(ctx context.Context, a Bot) (Bot, error) {
	a.Name = strings.TrimSpace(a.Name)
	if n := utf8.RuneCountInString(a.Name); n < 1 || n > 40 {
		return a, fmt.Errorf("%w: name must have 1 to 40 characters", ErrInvalid)
	}
	if !slices.Contains(thinkingLevels, a.ThinkingLevel) {
		return a, fmt.Errorf("%w: unknown thinking level %q", ErrInvalid, a.ThinkingLevel)
	}
	a.RunnerID = strings.TrimSpace(a.RunnerID)
	if a.RunnerID == "local" {
		a.RunnerID = ""
	}
	if a.RunnerID != "" {
		if _, err := s.q.GetRunner(ctx, a.RunnerID); errors.Is(err, sql.ErrNoRows) {
			return a, fmt.Errorf("%w: execution environment %q does not exist", ErrInvalid, a.RunnerID)
		} else if err != nil {
			return a, err
		}
	}
	a.Provider = strings.TrimSpace(a.Provider)
	a.ModelID = strings.TrimSpace(a.ModelID)
	if a.ModelID != "" {
		if err := s.checkModel(ctx, a.Provider, a.ModelID); err != nil {
			return a, err
		}
	} else {
		a.Provider = ""
	}
	a.FallbackProvider = strings.TrimSpace(a.FallbackProvider)
	a.FallbackModelID = strings.TrimSpace(a.FallbackModelID)
	if a.FallbackModelID != "" {
		if err := s.checkModel(ctx, a.FallbackProvider, a.FallbackModelID); err != nil {
			return a, err
		}
	} else {
		a.FallbackProvider = ""
	}
	if a.FallbackModelID != "" && a.ModelID == "" {
		return a, fmt.Errorf("%w: choose a model before a fallback model", ErrInvalid)
	}
	return a, nil
}

// TelegramConfig returns the saved bot configuration, or nil when Telegram is off.
func (s *Service) TelegramConfig(ctx context.Context, botID string) (*telegram.Config, error) {
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
	var options telegram.Options
	if err := json.Unmarshal([]byte(row.Options), &options); err != nil {
		return nil, err
	}
	cfg := &telegram.Config{Options: options, Token: row.Token, AllowedUsers: users, APIURL: s.apiURL, Pair: s.pairTelegram(botID, row.Token), RetryAt: row.RetryAt,
		Webhook: telegram.WebhookConfig{URL: row.WebhookUrl, SecretToken: row.WebhookSecret}}
	if strings.Contains(row.Token, telegramVaultMarker) {
		cfg.CredentialRef = row.Token
		cfg.Token, err = s.telegramVault.Credential(row.Token)
		if err != nil {
			return nil, err
		}
		if cfg.Token == "" {
			return nil, errors.New("the saved managed Telegram credential is missing")
		}
	}
	return cfg, nil
}

// SetTelegram saves the bot configuration and restarts the bot. An empty token keeps the saved
// token.
func (s *Service) SetTelegram(ctx context.Context, botID string, token string, allowedUsers []int64, options *telegram.Options, transport string, webhook *telegram.WebhookConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setTelegram(ctx, botID, token, allowedUsers, options, transport, webhook)
}

func (s *Service) setTelegram(ctx context.Context, botID, token string, allowedUsers []int64, options *telegram.Options, transport string, webhook *telegram.WebhookConfig) error {
	if err := db.RequireActive(ctx, s.q, botID); err != nil {
		return err
	}
	// Pairing uses TryLock, so this snapshot cannot change before the old bot joins.
	saved, err := s.TelegramConfig(ctx, botID)
	if err != nil {
		return err
	}
	if token == "" {
		if saved != nil {
			token = saved.Token
		}
	}
	cfg := telegram.Config{Token: strings.TrimSpace(token), AllowedUsers: allowedUsers, APIURL: s.apiURL}
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
		cfg.Webhook = telegram.WebhookConfig{}
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
	if err := s.checkCredential(ctx, botID, "telegram", cfg.Token); err != nil {
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
	s.onTelegram(botID, nil)
	committed := false
	defer func() {
		if !committed && saved != nil {
			s.onTelegram(botID, saved)
		}
	}()
	if saved != nil && saved.Webhook.URL != "" && (!sameBot || cfg.Webhook.URL == "") {
		teardown := *saved
		if sameBot {
			teardown.Token = cfg.Token
		}
		if err := telegram.DeleteWebhook(ctx, teardown); err != nil {
			return err
		}
	}
	cfg.RetryAt, err = s.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{BotID: botID,
		Token: key, AllowedUsers: strings.Join(users, ","), Options: string(encoded),
		WebhookUrl: cfg.Webhook.URL, WebhookSecret: cfg.Webhook.SecretToken,
	})
	if err != nil {
		return err
	}
	committed = true
	s.onTelegram(botID, &cfg)
	if saved != nil && saved.CredentialRef != cfg.CredentialRef {
		s.forgetTelegramCredential(saved.CredentialRef)
	}
	return nil
}

// DeleteTelegram turns Telegram off and forgets the token.
func (s *Service) DeleteTelegram(ctx context.Context, botID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteTelegram(ctx, botID)
}

func (s *Service) deleteTelegram(ctx context.Context, botID string) error {
	saved, err := s.TelegramConfig(ctx, botID)
	if err != nil {
		return err
	}
	s.onTelegram(botID, nil)
	if saved != nil && saved.Webhook.URL != "" {
		if err := telegram.DeleteWebhook(ctx, *saved); err != nil {
			s.onTelegram(botID, saved)
			return err
		}
	}
	if err := s.q.DeleteTelegramSettings(ctx, botID); err != nil {
		if saved != nil {
			s.onTelegram(botID, saved)
		}
		return err
	}
	s.onTelegram(botID, nil)
	if saved != nil {
		s.forgetTelegramCredential(saved.CredentialRef)
	}
	return nil
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

// Home returns the storage root for a bot in the resolved data directory.
func Home(dataDir, id string) string {
	return filepath.Join(dataDir, "agents", id)
}
