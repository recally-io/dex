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
	"unicode/utf8"
	"uuid"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/feishu"
	"github.com/recally-io/dex/internal/telegram"
	"github.com/recally-io/dex/internal/wechat"
	"github.com/recally-io/dex/internal/worker"
)

// ErrInvalid wraps the errors of a change that the service refuses.
var ErrInvalid = errors.New("invalid settings")

// ErrNotFound means that the agent does not exist.
var ErrNotFound = errors.New("agent not found")

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
	APIs map[string]string `json:"apis"`
}

// Activity summarizes one agent for the agent list.
type Activity struct {
	ActiveSessions     int64 `json:"activeSessions"`
	RunningChannels    int64 `json:"runningChannels"`
	EnabledAutomations int64 `json:"enabledAutomations"`
}

// Agent holds the settings of one agent. An empty ModelID means no model is chosen.
type Agent struct {
	Activity      *Activity `json:"activity,omitzero"`
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Provider      string    `json:"provider"`
	ModelID       string    `json:"modelId"`
	ThinkingLevel string    `json:"thinkingLevel"`
	Instructions  string    `json:"instructions"`
}

// thinkingLevels are pi-ai's ModelThinkingLevel values. pi-ai sends the nearest level that the
// model supports, so any level is valid for any model.
var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

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
	// ModelCount is the number of models that the worker has for the provider.
	ModelCount int `json:"modelCount"`
	// Error is why the worker could not list the provider's models, or empty.
	Error string `json:"error,omitempty"`
}

// Telegram describes the bot without its token.
type Telegram struct {
	Status       channel.Status `json:"status"`
	Enabled      bool           `json:"enabled"`
	BotID        string         `json:"botId,omitempty"`
	AllowedUsers []int64        `json:"allowedUsers"`
}

// Service reads and changes settings.
type Service struct {
	AgentCreated  func(string)
	wechatOptions wechat.Options
	onWechat      func(string, *wechat.Config)
	wechatDB      *sql.DB
	ChannelStatus func(string, string) channel.Status
	q             *sqlc.Queries
	worker        worker.CallFunc
	apiURL        string // Telegram Bot API URL; empty for the default
	onTelegram    func(string, *telegram.Config)

	feishuAPIURL    string
	feishuRegistrar feishu.Registrar
	onFeishu        func(string, *feishu.Config)

	// Serializes changes, so the worker and the bot always get the newest saved settings.
	mu       sync.Mutex
	sessions map[string]*channelSession

	errMu sync.Mutex
	errs  map[string]string // from the last ApplyProviders: provider ID → why its model list failed
}

// New returns the shared settings Service. onTelegram receives the new bot configuration after each
// Telegram change, or nil when Telegram is off.
func New(q *sqlc.Queries, call worker.CallFunc, telegramAPIURL string, onTelegram func(string, *telegram.Config)) *Service {
	return &Service{q: q, worker: call, apiURL: telegramAPIURL, onTelegram: onTelegram}
}

// Get returns all settings. It asks the worker which providers exist.
func (s *Service) Get(ctx context.Context) (Settings, error) {
	agent, err := s.Agent(ctx, "default")
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
	models, err := s.Models(ctx)
	if err != nil {
		return Settings{}, err
	}
	providers := make([]Provider, len(rows))
	for i, r := range rows {
		providers[i] = s.provider(r, models)
		// pi-ai owns the names of built-in providers.
		if j := slices.IndexFunc(builtins, func(b worker.ProviderInfo) bool { return b.ID == r.ID }); r.Api == "" && j >= 0 {
			providers[i].Name = builtins[j].Name
		}
	}
	return Settings{
		Agent:     agent,
		Providers: providers,
		Builtins:  builtins,
		APIs:      APIs,
	}, nil
}

// provider returns the view of a saved provider, with its model count and its last error.
func (s *Service) provider(r sqlc.Provider, models []worker.ModelInfo) Provider {
	s.errMu.Lock()
	msg := s.errs[r.ID]
	s.errMu.Unlock()
	count := 0
	for _, m := range models {
		if m.Provider == r.ID {
			count++
		}
	}
	return Provider{
		ID: r.ID, Name: r.Name, API: r.Api, BaseURL: r.BaseUrl, KeySet: r.ApiKey != "", Models: splitModels(r.Models),
		ModelCount: count, Error: msg,
	}
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

// Agent reads one agent.
func (s *Service) Agent(ctx context.Context, id string) (Agent, error) {
	row, err := s.q.GetAgent(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	return agentView(row), err
}

func agentView(row sqlc.Agent) Agent {
	return Agent{ID: row.ID, Name: row.Name, Provider: row.ModelProvider, ModelID: row.ModelID,
		ThinkingLevel: row.ThinkingLevel, Instructions: row.Instructions}
}

// ListAgents returns agents and their activity in creation order.
func (s *Service) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.q.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := s.q.ListAgentActivity(ctx)
	if err != nil {
		return nil, err
	}
	activity := make(map[string]Activity, len(counts))
	for _, count := range counts {
		activity[count.ID] = Activity{ActiveSessions: count.ActiveSessions, EnabledAutomations: count.EnabledAutomations}
	}
	agents := make([]Agent, len(rows))
	for i, row := range rows {
		agents[i] = agentView(row)
		current := activity[row.ID]
		if s.ChannelStatus != nil {
			for _, platform := range []string{"telegram", "wechat", "feishu"} {
				if s.ChannelStatus(row.ID, platform).State == "running" {
					current.RunningChannels++
				}
			}
		}
		agents[i].Activity = &current
	}
	return agents, nil
}

// SaveAgent changes one agent. Its next message uses the settings.
func (s *Service) SaveAgent(ctx context.Context, id string, a Agent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.Agent(ctx, id); err != nil {
		return err
	}
	a, err := s.cleanAgent(ctx, a)
	if err != nil {
		return err
	}
	return s.q.UpdateAgentSettings(ctx, sqlc.UpdateAgentSettingsParams{
		ModelProvider: a.Provider, ModelID: a.ModelID, ThinkingLevel: a.ThinkingLevel, Instructions: a.Instructions,
		ID: id, Name: a.Name,
	})
}

// CreateAgent stores an agent without changing global provider configuration.
func (s *Service) CreateAgent(ctx context.Context, a Agent) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.cleanAgent(ctx, a)
	if err != nil {
		return Agent{}, err
	}
	row, err := s.q.CreateAgent(ctx, sqlc.CreateAgentParams{
		ID: uuid.NewV7().String(), Name: a.Name, ModelProvider: a.Provider, ModelID: a.ModelID,
		ThinkingLevel: a.ThinkingLevel, Instructions: a.Instructions, CreatedAt: time.Now().UnixMilli(),
	})
	if err == nil && s.AgentCreated != nil {
		s.AgentCreated(row.ID)
	}
	return agentView(row), err
}

func (s *Service) cleanAgent(ctx context.Context, a Agent) (Agent, error) {
	a.Name = strings.TrimSpace(a.Name)
	if n := utf8.RuneCountInString(a.Name); n < 1 || n > 40 {
		return a, fmt.Errorf("%w: name must have 1 to 40 characters", ErrInvalid)
	}
	if !slices.Contains(thinkingLevels, a.ThinkingLevel) {
		return a, fmt.Errorf("%w: unknown thinking level %q", ErrInvalid, a.ThinkingLevel)
	}
	if a.ModelID != "" {
		if err := s.checkModel(ctx, a.Provider, a.ModelID); err != nil {
			return a, err
		}
	} else {
		a.Provider = ""
	}
	return a, nil
}

func (s *Service) checkModel(ctx context.Context, provider, modelID string) error {
	models, err := s.Models(ctx)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(models, func(m worker.ModelInfo) bool { return m.Provider == provider && m.ID == modelID }) {
		return fmt.Errorf("%w: no model %s/%s with credentials", ErrInvalid, provider, modelID)
	}
	return nil
}

// CheckChat refuses chat settings that SetChat would refuse. A new chat stores its settings with
// its first message, so the caller checks them first.
func (s *Service) CheckChat(ctx context.Context, c chat.Settings) error {
	if c.ThinkingLevel != "" && !slices.Contains(thinkingLevels, c.ThinkingLevel) {
		return fmt.Errorf("%w: unknown thinking level %q", ErrInvalid, c.ThinkingLevel)
	}
	if (c.Provider == "") != (c.ModelID == "") {
		return fmt.Errorf("%w: give both the provider and the model ID, or neither", ErrInvalid)
	}
	if c.ModelID == "" {
		return nil
	}
	return s.checkModel(ctx, c.Provider, c.ModelID)
}

// SetChat replaces the settings that chat chatID overrides. The next message of the chat uses them.
func (s *Service) SetChat(ctx context.Context, agentID string, chatID int64, c chat.Settings) error {
	if err := s.CheckChat(ctx, c); err != nil {
		return err
	}
	return s.q.SetConversationSettings(ctx, sqlc.SetConversationSettingsParams{
		ModelProvider: c.Provider, ModelID: c.ModelID, ThinkingLevel: c.ThinkingLevel, ID: chatID, AgentID: agentID,
	})
}

// SetProvider adds provider p or changes it, gives all providers to the worker, and returns the
// saved provider. An empty APIKey keeps the saved key. When the worker cannot list the models of
// p, p stays saved and the returned provider has the reason in Error.
func (s *Service) SetProvider(ctx context.Context, p Provider) (Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	builtins, err := s.builtins(ctx)
	if err != nil {
		return Provider{}, err
	}
	i := slices.IndexFunc(builtins, func(b worker.ProviderInfo) bool { return b.ID == p.ID })
	if p.API == "" {
		if i < 0 {
			return Provider{}, fmt.Errorf("%w: unknown built-in provider %q", ErrInvalid, p.ID)
		}
		p.Name, p.BaseURL, p.Models = builtins[i].Name, "", nil
	} else {
		if _, ok := APIs[p.API]; !ok {
			return Provider{}, fmt.Errorf("%w: unknown API %q", ErrInvalid, p.API)
		}
		if !customID.MatchString(p.ID) {
			return Provider{}, fmt.Errorf("%w: the ID must be 1 to 40 lowercase letters, digits, or hyphens", ErrInvalid)
		}
		if i >= 0 {
			return Provider{}, fmt.Errorf("%w: %q is a built-in provider; choose another ID", ErrInvalid, p.ID)
		}
		if err := checkBaseURL(p.BaseURL); err != nil {
			return Provider{}, err
		}
		p.Name = cmp.Or(p.Name, p.ID)
	}

	saved, err := s.q.GetProvider(ctx, p.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		saved.CreatedAt = time.Now().UnixMilli()
	case err != nil:
		return Provider{}, err
	}
	p.APIKey = cmp.Or(p.APIKey, saved.ApiKey)
	if p.API == "" && p.APIKey == "" {
		return Provider{}, fmt.Errorf("%w: enter an API key", ErrInvalid)
	}
	row := sqlc.UpsertProviderParams{
		ID: p.ID, Name: p.Name, Api: p.API, BaseUrl: p.BaseURL, ApiKey: p.APIKey,
		Models: strings.Join(cleanModels(p.Models), ","), CreatedAt: saved.CreatedAt,
	}
	if err := s.q.UpsertProvider(ctx, row); err != nil {
		return Provider{}, err
	}
	if _, err := s.ApplyProviders(ctx, s.worker); err != nil {
		return Provider{}, fmt.Errorf("saved, but the worker did not apply it: %w", err)
	}
	models, err := s.Models(ctx)
	if err != nil {
		return Provider{}, err
	}
	return s.provider(sqlc.Provider(row), models), nil
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
	s.errMu.Lock()
	s.errs = result.Errors
	s.errMu.Unlock()
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
func (s *Service) TelegramConfig(ctx context.Context, agentID string) (*telegram.Config, error) {
	row, err := s.q.GetTelegramSettings(ctx, agentID)
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
	return &telegram.Config{Token: row.Token, AllowedUsers: users, APIURL: s.apiURL, Pair: s.pairTelegram(agentID, row.Token)}, nil
}

// SetTelegram saves the bot configuration and restarts the bot. An empty token keeps the saved
// token.
func (s *Service) SetTelegram(ctx context.Context, agentID string, token string, allowedUsers []int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == "" {
		saved, err := s.TelegramConfig(ctx, agentID)
		if err != nil {
			return err
		}
		if saved != nil {
			token = saved.Token
		}
	}
	cfg := telegram.Config{Token: strings.TrimSpace(token), AllowedUsers: allowedUsers, APIURL: s.apiURL}
	cfg.Pair = s.pairTelegram(agentID, cfg.Token)
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := s.checkCredential(ctx, agentID, "telegram", cfg.Token); err != nil {
		return err
	}
	users := make([]string, len(allowedUsers))
	for i, id := range allowedUsers {
		users[i] = strconv.FormatInt(id, 10)
	}
	err := s.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{AgentID: agentID,
		Token: cfg.Token, AllowedUsers: strings.Join(users, ","),
	})
	if err != nil {
		return err
	}
	s.onTelegram(agentID, &cfg)
	return nil
}

// DeleteTelegram turns Telegram off and forgets the token.
func (s *Service) DeleteTelegram(ctx context.Context, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.q.DeleteTelegramSettings(ctx, agentID); err != nil {
		return err
	}
	s.onTelegram(agentID, nil)
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
