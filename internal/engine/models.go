package engine

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
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
	custom      map[string]bool
	mu          sync.Mutex
}

func NewCatalog(fake bool) *Catalog {
	credentials := ai.NewInMemoryCredentialStore()
	c := &Catalog{credentials: credentials, custom: map[string]bool{}, Models: ai.BuiltinModels(ai.CreateModelsOptions{Credentials: credentials, AuthContext: &ai.AuthContext{Env: func(string) (string, bool) { return "", false }, FileExists: func(string) bool { return false }}})}
	if fake {
		c.Models.SetProvider(fakeProvider())
	}
	return c
}
func (c *Catalog) Configure(ctx context.Context, providers []ProviderSettings) (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range providers {
		if p.API != "" && p.API != "openai-completions" && p.API != "openai-responses" && p.API != "anthropic-messages" {
			return nil, fmt.Errorf("unsupported provider API")
		}
	}
	stored, err := c.credentials.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range stored {
		if err := c.credentials.Delete(ctx, p.ProviderID); err != nil {
			return nil, err
		}
	}
	for id := range c.custom {
		c.Models.DeleteProvider(id)
	}
	clear(c.custom)
	listed := []string{}
	for _, p := range providers {
		if p.API == "" {
			if _, err := c.credentials.Modify(ctx, p.ID, func(*ai.Credential) (*ai.Credential, error) {
				return &ai.Credential{Type: ai.CredentialType("api_key"), Key: p.APIKey}, nil
			}); err != nil {
				return nil, err
			}
			continue
		}
		c.Models.SetProvider(customProvider(p)) //nolint:contextcheck // Refresh callbacks use PiG's operation context, not this Configure request.
		c.custom[p.ID] = true
		if len(p.Models) == 0 {
			listed = append(listed, p.ID)
		}
	}
	failures := map[string]string{}
	if len(listed) > 0 {
		result := c.Models.Refresh(ctx, ai.ModelsRefreshOptions{Providers: listed, AllowNetwork: new(true)})
		for id, err := range result.Errors {
			failures[id] = err.Error()
		}
	}
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	return failures, nil
}
func (c *Catalog) Builtins(ctx context.Context) ([]ProviderInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []ProviderInfo{}
	for _, p := range c.Models.GetProviders() {
		if p.Auth.APIKey == nil || p.ID == "faux" || c.custom[p.ID] {
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
			// A stored endpoint must not block startup while the caller remains alive.
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
	return ai.CreateProvider(opts)
}

// Keep sensitive runtime details available to error matching, not error text.
type privateError struct {
	message string
	cause   error
}

func (e *privateError) Error() string { return e.message }
func (e *privateError) Unwrap() error { return e.cause }
