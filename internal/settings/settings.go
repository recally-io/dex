// Package settings reads and changes the settings that the web UI edits: the agent's model and
// instructions, provider credentials, and the Telegram bot. control.sqlite is their only source.
// A change is saved first and then applied to the worker or the bot.
package settings

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vaayne/dex/internal/db/sqlc"
	"github.com/vaayne/dex/internal/telegram"
	"github.com/vaayne/dex/internal/worker"
)

// ErrInvalid wraps the errors of a change that the service refuses.
var ErrInvalid = errors.New("invalid settings")

// APIs are the APIs a custom provider can use, with their names for the UI.
var APIs = map[string]string{
	"openai-completions": "OpenAI Chat Completions",
	"openai-responses":   "OpenAI Responses",
	"anthropic-messages": "Anthropic Messages",
}

// A custom provider ID becomes a pi-ai provider ID and the first part of a model reference.
var customID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// Settings is the complete view for the web UI. It never holds a secret.
type Settings struct {
	Agent Agent `json:"agent"`
	// Providers are the providers that the user added, in the order of addition.
	Providers []Provider `json:"providers"`
	// Builtins are all pi-ai built-in providers that take an API key, for the UI to add one.
	Builtins []worker.ProviderInfo `json:"builtins"`
	// APIs are the APIs of custom providers: ID → name.
	APIs     map[string]string `json:"apis"`
	Telegram Telegram          `json:"telegram"`
}

// Agent holds the settings of the default agent. An empty ModelID means no model is chosen.
type Agent struct {
	Provider     string `json:"provider"`
	ModelID      string `json:"modelId"`
	Instructions string `json:"instructions"`
}

// Provider is a provider that the user added. API is empty for a pi-ai built-in provider. The API
// key is write-only: requests send it, responses report only whether one is saved.
type Provider struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	API     string   `json:"api"`
	BaseURL string   `json:"baseUrl"`
	APIKey  string   `json:"apiKey,omitempty"`
	KeySet  bool     `json:"keySet"`
	Models  []string `json:"models"`
}

// Telegram describes the bot without its token.
type Telegram struct {
	Enabled      bool    `json:"enabled"`
	BotID        string  `json:"botId,omitempty"`
	AllowedUsers []int64 `json:"allowedUsers"`
}

// Service reads and changes settings.
type Service struct {
	q          *sqlc.Queries
	worker     worker.CallFunc
	agentID    string
	apiURL     string // Telegram Bot API URL; empty for the default
	onTelegram func(*telegram.Config)

	// Serializes changes, so the worker and the bot always get the newest saved settings.
	mu sync.Mutex
}

// New returns a Service for agentID. onTelegram receives the new bot configuration after each
// Telegram change, or nil when Telegram is off.
func New(q *sqlc.Queries, call worker.CallFunc, agentID, telegramAPIURL string, onTelegram func(*telegram.Config)) *Service {
	return &Service{q: q, worker: call, agentID: agentID, apiURL: telegramAPIURL, onTelegram: onTelegram}
}

// Get returns all settings. It asks the worker which providers exist.
func (s *Service) Get(ctx context.Context) (Settings, error) {
	agent, err := s.q.GetAgent(ctx, s.agentID)
	if err != nil {
		return Settings{}, err
	}
	rows, err := s.q.ListProviders(ctx)
	if err != nil {
		return Settings{}, err
	}
	builtins, err := s.builtins(ctx)
	if err != nil {
		return Settings{}, err
	}
	providers := make([]Provider, len(rows))
	for i, r := range rows {
		providers[i] = Provider{
			ID: r.ID, Name: r.Name, API: r.Api, BaseURL: r.BaseUrl, KeySet: r.ApiKey != "", Models: splitModels(r.Models),
		}
		// pi-ai owns the names of built-in providers.
		if j := slices.IndexFunc(builtins, func(b worker.ProviderInfo) bool { return b.ID == r.ID }); r.Api == "" && j >= 0 {
			providers[i].Name = builtins[j].Name
		}
	}
	tg := Telegram{AllowedUsers: []int64{}}
	if cfg, err := s.TelegramConfig(ctx); err != nil {
		return Settings{}, err
	} else if cfg != nil {
		botID, _, _ := strings.Cut(cfg.Token, ":")
		tg = Telegram{Enabled: true, BotID: botID, AllowedUsers: cfg.AllowedUsers}
	}
	return Settings{
		Agent:     Agent{Provider: agent.ModelProvider, ModelID: agent.ModelID, Instructions: agent.Instructions},
		Providers: providers,
		Builtins:  builtins,
		APIs:      APIs,
		Telegram:  tg,
	}, nil
}

func (s *Service) builtins(ctx context.Context) ([]worker.ProviderInfo, error) {
	var listed worker.ListProvidersResult
	if err := s.worker(ctx, "listProviders", struct{}{}, &listed); err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	return listed.Providers, nil
}

// Models returns the models of the providers that have credentials.
func (s *Service) Models(ctx context.Context) ([]worker.ModelInfo, error) {
	var listed worker.ListModelsResult
	if err := s.worker(ctx, "listModels", struct{}{}, &listed); err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	return listed.Models, nil
}

// SetAgent changes the agent's model and instructions. The next message in each chat uses them.
// An empty model clears it.
func (s *Service) SetAgent(ctx context.Context, a Agent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.ModelID != "" {
		models, err := s.Models(ctx)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(models, func(m worker.ModelInfo) bool {
			return m.Provider == a.Provider && m.ID == a.ModelID
		}) {
			return fmt.Errorf("%w: no model %s/%s with credentials", ErrInvalid, a.Provider, a.ModelID)
		}
	} else {
		a.Provider = ""
	}
	return s.q.UpdateAgentSettings(ctx, sqlc.UpdateAgentSettingsParams{
		ModelProvider: a.Provider, ModelID: a.ModelID, Instructions: a.Instructions, ID: s.agentID,
	})
}

// SetProvider adds provider p or changes it, and gives all providers to the worker. An empty
// APIKey keeps the saved key. When the worker cannot list the models of p, p stays saved and
// SetProvider returns the reason.
func (s *Service) SetProvider(ctx context.Context, p Provider) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	builtins, err := s.builtins(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(builtins, func(b worker.ProviderInfo) bool { return b.ID == p.ID })
	if p.API == "" {
		if i < 0 {
			return fmt.Errorf("%w: unknown built-in provider %q", ErrInvalid, p.ID)
		}
		p.Name, p.BaseURL, p.Models = builtins[i].Name, "", nil
	} else {
		if _, ok := APIs[p.API]; !ok {
			return fmt.Errorf("%w: unknown API %q", ErrInvalid, p.API)
		}
		if !customID.MatchString(p.ID) {
			return fmt.Errorf("%w: the ID must be 1 to 40 lowercase letters, digits, or hyphens", ErrInvalid)
		}
		if i >= 0 {
			return fmt.Errorf("%w: %q is a built-in provider; choose another ID", ErrInvalid, p.ID)
		}
		if err := checkBaseURL(p.BaseURL); err != nil {
			return err
		}
		p.Name = cmp.Or(p.Name, p.ID)
	}

	saved, err := s.q.GetProvider(ctx, p.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		saved.CreatedAt = time.Now().UnixMilli()
	case err != nil:
		return err
	}
	p.APIKey = cmp.Or(p.APIKey, saved.ApiKey)
	if p.API == "" && p.APIKey == "" {
		return fmt.Errorf("%w: enter an API key", ErrInvalid)
	}
	err = s.q.UpsertProvider(ctx, sqlc.UpsertProviderParams{
		ID: p.ID, Name: p.Name, Api: p.API, BaseUrl: p.BaseURL, ApiKey: p.APIKey,
		Models: strings.Join(cleanModels(p.Models), ","), CreatedAt: saved.CreatedAt,
	})
	if err != nil {
		return err
	}
	errs, err := s.ApplyProviders(ctx, s.worker)
	if err != nil {
		return fmt.Errorf("saved, but the worker did not apply it: %w", err)
	}
	if msg, ok := errs[p.ID]; ok {
		return fmt.Errorf("saved, but listing the models failed: %s", msg)
	}
	return nil
}

// DeleteProvider removes provider id.
func (s *Service) DeleteProvider(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.q.DeleteProvider(ctx, id); err != nil {
		return err
	}
	if _, err := s.ApplyProviders(ctx, s.worker); err != nil {
		return fmt.Errorf("saved, but the worker did not apply it: %w", err)
	}
	return nil
}

// ApplyProviders gives all saved providers to the worker that call reaches. It returns, per
// provider ID, why the worker could not list its models.
func (s *Service) ApplyProviders(ctx context.Context, call worker.CallFunc) (map[string]string, error) {
	rows, err := s.q.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	params := worker.ConfigureParams{Providers: make([]worker.ProviderSettings, len(rows))}
	for i, r := range rows {
		params.Providers[i] = worker.ProviderSettings{
			ID: r.ID, Name: r.Name, API: r.Api, BaseURL: r.BaseUrl, APIKey: r.ApiKey, Models: splitModels(r.Models),
		}
	}
	var result worker.ConfigureResult
	if err := call(ctx, "configure", params, &result); err != nil {
		return nil, err
	}
	return result.Errors, nil
}

// checkBaseURL accepts an absolute http or https URL without user info, so the API key is the
// only credential and the URL can appear in logs.
func checkBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return fmt.Errorf("%w: the base URL must be an http or https URL without a user name, for example http://127.0.0.1:3425/v1", ErrInvalid)
	}
	return nil
}

func cleanModels(models []string) []string {
	var out []string
	for _, m := range models {
		if m = strings.TrimSpace(m); m != "" && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

func splitModels(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

// TelegramConfig returns the saved bot configuration, or nil when Telegram is off.
func (s *Service) TelegramConfig(ctx context.Context) (*telegram.Config, error) {
	row, err := s.q.GetTelegramSettings(ctx)
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
	return &telegram.Config{Token: row.Token, AllowedUsers: users, APIURL: s.apiURL}, nil
}

// SetTelegram saves the bot configuration and restarts the bot. An empty token keeps the saved
// token.
func (s *Service) SetTelegram(ctx context.Context, token string, allowedUsers []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == "" {
		saved, err := s.TelegramConfig(ctx)
		if err != nil {
			return err
		}
		if saved != nil {
			token = saved.Token
		}
	}
	cfg := telegram.Config{Token: strings.TrimSpace(token), AllowedUsers: allowedUsers, APIURL: s.apiURL}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	users := make([]string, len(allowedUsers))
	for i, id := range allowedUsers {
		users[i] = strconv.FormatInt(id, 10)
	}
	err := s.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{
		Token: cfg.Token, AllowedUsers: strings.Join(users, ","),
	})
	if err != nil {
		return err
	}
	s.onTelegram(&cfg)
	return nil
}

// DeleteTelegram turns Telegram off and forgets the token.
func (s *Service) DeleteTelegram(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.q.DeleteTelegramSettings(ctx); err != nil {
		return err
	}
	s.onTelegram(nil)
	return nil
}

func parseUsers(s string) ([]int64, error) {
	var users []int64
	for field := range strings.SplitSeq(s, ",") {
		id, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("telegram_settings.allowed_users: %w", err)
		}
		users = append(users, id)
	}
	return users, nil
}
