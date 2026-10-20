package engine

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
)

type ProviderSettings struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	API     string   `json:"api"`
	BaseURL string   `json:"baseUrl"`
	APIKey  string   `json:"apiKey"`
	Models  []string `json:"models"`
}
type ProviderInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Source string `json:"source,omitempty"`
}
type ModelInfo struct {
	Provider       string                  `json:"provider"`
	ID             string                  `json:"id"`
	Name           string                  `json:"name"`
	Reasoning      bool                    `json:"reasoning"`
	Input          []ai.ModelInputModality `json:"input"`
	ThinkingLevels []ai.ModelThinkingLevel `json:"thinkingLevels"`
}
type Catalog struct {
	Models      *ai.Models
	credentials *ai.InMemoryCredentialStore
	custom      map[string]*providerRegistration
	closed      bool
	work        sync.WaitGroup
	mu          sync.Mutex
}

func NewCatalog(fake bool) *Catalog {
	credentials := ai.NewInMemoryCredentialStore()
	c := &Catalog{credentials: credentials, custom: map[string]*providerRegistration{}, Models: ai.BuiltinModels(ai.CreateModelsOptions{Credentials: credentials, AuthContext: &ai.AuthContext{Env: func(string) (string, bool) { return "", false }, FileExists: func(string) bool { return false }}})}
	if fake {
		c.Models.SetProvider(fakeProvider())
	}
	return c
}

// ErrModelsPending tells callers to retry after model discovery finishes.
var ErrModelsPending = errors.New("model discovery is pending")

type providerRegistration struct {
	settings  ProviderSettings
	discovery *Discovery
	cancel    context.CancelFunc
}

// Discovery is the completion of one configuration operation.
// Results belong to that operation, even if another operation replaces its providers.
type Discovery struct {
	done     chan struct{}
	failures map[string]string
	err      error
}

// Done closes when this operation's discovery has finished.
func (d *Discovery) Done() <-chan struct{} { return d.done }

// Wait waits for this operation without holding a catalog or engine lock.
func (d *Discovery) Wait(ctx context.Context) (map[string]string, error) {
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-d.done:
		return d.failures, d.err
	}
}

// Configure registers providers and waits for discovery, for synchronous callers.
func (c *Catalog) Configure(ctx context.Context, providers []ProviderSettings) (map[string]string, error) {
	discovery, err := c.ConfigureAsync(ctx, providers)
	if err != nil {
		return nil, err
	}
	return discovery.Wait(ctx)
}

// ConfigureAsync registers credentials and static models before returning.
// New, changed, and failed dynamic providers need discovery. Other providers keep their catalogs.
func (c *Catalog) ConfigureAsync(ctx context.Context, providers []ProviderSettings) (*Discovery, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, p := range providers {
		if p.API != "" && p.API != "openai-completions" && p.API != "openai-responses" && p.API != "anthropic-messages" {
			return nil, fmt.Errorf("unsupported provider API")
		}
	}
	desired := make(map[string]ProviderSettings, len(providers))
	for _, p := range providers {
		desired[p.ID] = p
	}
	stored, err := c.credentials.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, credential := range stored {
		if p, ok := desired[credential.ProviderID]; !ok || p.API != "" {
			if err := c.credentials.Delete(ctx, credential.ProviderID); err != nil {
				return nil, err
			}
		}
	}
	for id, current := range c.custom {
		if p, ok := desired[id]; !ok || p.API == "" {
			current.cancel()
			<-current.discovery.done
			c.Models.DeleteProvider(id)
			delete(c.custom, id)
		}
	}
	changes := map[string]*Discovery{}
	for _, p := range providers {
		if p.API == "" {
			if _, err := c.credentials.Modify(ctx, p.ID, func(*ai.Credential) (*ai.Credential, error) {
				return &ai.Credential{Type: ai.CredentialAPIKey, Key: p.APIKey}, nil
			}); err != nil {
				return nil, err
			}
			continue
		}
		current := c.custom[p.ID]
		if current != nil && sameProviderSettings(current.settings, p) {
			select {
			case <-current.discovery.done:
				if current.discovery.err == nil && len(current.discovery.failures) == 0 {
					continue
				}
			default:
				continue
			}
		}
		if current != nil {
			current.cancel()
			// Refresh returns on cancellation before network callbacks finish.
			// Join its registration loop before a replacement can start a newer generation.
			<-current.discovery.done
		}
		// Keep our own copy because callers can reuse their settings slice.
		p.Models = slices.Clone(p.Models)
		discovery := &Discovery{done: make(chan struct{}), failures: map[string]string{}}
		refreshCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		c.custom[p.ID] = &providerRegistration{settings: p, discovery: discovery, cancel: cancel}
		c.Models.SetProvider(customProvider(p)) //nolint:contextcheck // Refresh callbacks use PiG's operation context.
		if len(p.Models) > 0 {
			close(discovery.done)
			cancel()
			continue
		}
		changes[p.ID] = discovery
		c.work.Go(func() {
			defer cancel()
			defer close(discovery.done)
			result := c.Models.Refresh(refreshCtx, ai.ModelsRefreshOptions{Providers: []string{p.ID}, AllowNetwork: new(true)})
			for id, err := range result.Errors {
				discovery.failures[id] = err.Error()
			}
			discovery.err = context.Cause(refreshCtx)
		})
	}
	operation := &Discovery{done: make(chan struct{}), failures: map[string]string{}}
	if len(changes) == 0 {
		close(operation.done)
		return operation, nil
	}
	c.work.Go(func() {
		defer close(operation.done)
		for id, discovery := range changes {
			<-discovery.done
			if msg := discovery.failures[id]; msg != "" {
				operation.failures[id] = msg
			}
			operation.err = errors.Join(operation.err, discovery.err)
		}
	})
	return operation, nil
}

func sameProviderSettings(a, b ProviderSettings) bool {
	return a.ID == b.ID && a.Name == b.Name && a.API == b.API && a.BaseURL == b.BaseURL && a.APIKey == b.APIKey && slices.Equal(a.Models, b.Models)
}

// DiscoveryReady is a nonblocking gate for cold harness opens that can resume stored work.
func (c *Catalog) DiscoveryReady() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, current := range c.custom {
		select {
		case <-current.discovery.done:
		default:
			return ErrModelsPending
		}
	}
	return nil
}

// ModelReady distinguishes a pending catalog from an invalid model selection.
func (c *Catalog) ModelReady(provider string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if current := c.custom[provider]; current != nil {
		select {
		case <-current.discovery.done:
		default:
			return ErrModelsPending
		}
	}
	return nil
}

// Failure returns the current provider's sanitized discovery error.
func (c *Catalog) Failure(provider string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if current := c.custom[provider]; current != nil {
		select {
		case <-current.discovery.done:
			return current.discovery.failures[provider]
		default:
		}
	}
	return ""
}

// Close cancels discovery and joins both catalog and SDK work.
func (c *Catalog) Close() error {
	c.mu.Lock()
	c.closed = true
	for _, current := range c.custom {
		current.cancel()
	}
	c.mu.Unlock()
	c.work.Wait()
	c.Models.Close()
	return nil
}
func (c *Catalog) Builtins(ctx context.Context) ([]ProviderInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []ProviderInfo{}
	for _, p := range c.Models.GetProviders() {
		if p.Auth.APIKey == nil || p.ID == "faux" || c.custom[p.ID] != nil {
			continue
		}
		info := ProviderInfo{ID: p.ID, Name: p.Name}
		auth, err := c.Models.CheckAuth(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		if auth != nil {
			info.Source = auth.Source
			if info.Source == "stored credential" {
				info.Source = "settings"
			}
		}
		out = append(out, info)
	}
	return out, nil
}
func (c *Catalog) Available(ctx context.Context) ([]ModelInfo, error) {
	models, err := c.Models.GetAvailable(ctx)
	if err != nil {
		return nil, err
	}
	out := []ModelInfo{}
	for _, m := range models {
		out = append(out, ModelInfo{Provider: m.ProviderMeta.ProviderID, ID: m.ID, Name: m.DisplayName, Reasoning: m.ProviderMeta.Reasoning, Input: m.Input, ThinkingLevels: ai.GetSupportedThinkingLevels(m)})
	}
	return out, nil
}
func customProvider(p ProviderSettings) *ai.ModelsProvider {
	api := ai.OpenAICompletionsAPI()
	if p.API == "openai-responses" {
		api = ai.OpenAIResponsesAPI()
	}
	if p.API == "anthropic-messages" {
		api = ai.AnthropicMessagesAPI()
	}
	model := func(id string) *ai.Model {
		m := &ai.Model{ID: id, DisplayName: id, Input: []ai.ModelInputModality{"text"}, Capabilities: ai.ModelCapabilities{ContextWindow: 128000, MaxOutputTokens: 16384, SupportsToolUse: true}, ProviderMeta: ai.ProviderMetadata{ProviderID: p.ID, API: ai.API(p.API), BaseURL: p.BaseURL}}
		if p.API == "openai-responses" {
			m.ProviderMeta.Compat = &ai.ModelCompat{SupportsStrictMode: new(true)}
		}
		return m
	}
	opts := ai.CreateProviderOptions{ID: p.ID, Name: new(p.Name), BaseURL: p.BaseURL, API: api, Auth: ai.ProviderAuth{APIKey: &ai.APIKeyAuth{Name: p.Name + " API key", Resolve: func(context.Context, ai.APIKeyAuthInput) (*ai.AuthResult, error) {
		key := p.APIKey
		if key == "" {
			key = "none"
		}
		return &ai.AuthResult{Auth: ai.ModelAuth{APIKey: key}, Source: "settings"}, nil
	}}}}
	for _, id := range p.Models {
		opts.Models = append(opts.Models, model(id))
	}
	if len(p.Models) == 0 {
		opts.FetchModels = func(refresh ai.RefreshModelsContext) ([]ai.AnyModel, error) {
			// Bound each network request independently of the application lifetime.
			ctx, cancel := context.WithTimeout(refresh.Signal, 30*time.Second)
			defer cancel()
			suffix := "/models"
			if p.API == "anthropic-messages" {
				suffix = "/v1/models"
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.BaseURL, "/")+suffix, nil)
			if err != nil {
				return nil, &privateError{message: "invalid model discovery URL", cause: err}
			}
			if p.APIKey != "" {
				if p.API == "anthropic-messages" {
					req.Header.Set("x-api-key", p.APIKey)
					req.Header.Set("anthropic-version", "2023-06-01")
				} else {
					req.Header.Set("Authorization", "Bearer "+p.APIKey)
				}
			}
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				return nil, &privateError{message: "model discovery request failed", cause: err}
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("model discovery HTTP %d", response.StatusCode)
			}
			var list struct {
				Data *[]struct {
					ID        *string `json:"id"`
					Name      string  `json:"display_name"`
					Window    *int    `json:"context_window"`
					Tokens    *int    `json:"max_output_tokens"`
					Reasoning bool    `json:"reasoning"`
				} `json:"data"`
			}
			data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
			if err != nil {
				return nil, &privateError{message: "cannot read model discovery response", cause: err}
			}
			if err := json.Unmarshal(data, &list); err != nil {
				return nil, &privateError{message: "invalid model discovery response", cause: err}
			}
			if list.Data == nil {
				return nil, fmt.Errorf("model discovery response has no model list")
			}
			out := []ai.AnyModel{}
			for _, entry := range *list.Data {
				if entry.ID == nil {
					return nil, fmt.Errorf("model discovery entry has no id")
				}
				m := model(*entry.ID)
				if entry.Name != "" {
					m.DisplayName = entry.Name
				}
				if entry.Window != nil {
					m.Capabilities.ContextWindow = *entry.Window
				}
				if entry.Tokens != nil {
					m.Capabilities.MaxOutputTokens = *entry.Tokens
				}
				m.ProviderMeta.Reasoning = entry.Reasoning
				if entry.Reasoning {
					m.Capabilities.MaxThinking = ai.ThinkingLevelHigh
				}
				out = append(out, m)
			}
			return out, nil
		}
	}
	provider := ai.CreateProvider(opts)
	if refresh := provider.RefreshModels; refresh != nil {
		provider.RefreshModels = func(ctx ai.RefreshModelsContext) error {
			// SDK storage is keyed by ID, so a saved catalog can have a previous BaseURL.
			ctx.Stored = nil
			return refresh(ctx)
		}
	}
	return provider
}

// Keep sensitive runtime details available to error matching, not error text.
type privateError struct {
	message string
	cause   error
}

func (e *privateError) Error() string { return e.message }
func (e *privateError) Unwrap() error { return e.cause }
