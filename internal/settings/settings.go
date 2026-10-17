// Package settings owns global provider settings, model checks, and the access token.
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
	"strings"
	"sync"
	"time"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worker"
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
	// Providers are the providers that the user added, in the order of addition.
	Providers []Provider `json:"providers"`
	// Builtins are all pi-ai built-in providers that take an API key, for the UI to add one.
	Builtins []worker.ProviderInfo `json:"builtins"`
	// APIs are the APIs of custom providers: ID → name.
	APIs map[string]string `json:"apis"`
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

// Service reads and changes settings.
type Service struct {
	q      *sqlc.Queries
	worker worker.CallFunc
	// Serializes provider save and apply. Agent and channel changes have their own lock.
	mu    sync.Mutex
	errMu sync.Mutex
	errs  map[string]string
}

// New returns the global settings service.
func New(q *sqlc.Queries, call worker.CallFunc) *Service {
	return &Service{q: q, worker: call}
}

// Get returns all settings. It asks the worker which providers exist.
func (s *Service) Get(ctx context.Context) (Settings, error) {
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

// CheckModel keeps agent validation out of an incomplete provider update.
func (s *Service) CheckModel(ctx context.Context, provider, modelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkModel(ctx, provider, modelID)
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

// CheckChat checks chat overrides and task settings before the caller saves them.
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
