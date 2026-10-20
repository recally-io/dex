package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
)

func TestUserCatalogDiscoversModelsAndReplacesCredentials(t *testing.T) {
	// Given an ambient key that must not authorize models.
	t.Setenv("OPENAI_API_KEY", "parent-secret")
	catalog := NewCatalog(false)
	defer catalog.Models.Close()
	available, err := catalog.Available(t.Context())
	if err != nil || len(available) != 0 {
		t.Fatalf("ambient credential accepted: %v %v", available, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("discovery request: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"reason","display_name":"Reason","reasoning":true,"context_window":8192}]}`))
	}))
	defer server.Close()
	failures, err := catalog.Configure(t.Context(), []ProviderSettings{{ID: "local", Name: "Local", API: "openai-completions", BaseURL: server.URL, APIKey: "test-key"}})
	if err != nil || len(failures) != 0 {
		t.Fatalf("configure: %v %v", failures, err)
	}
	available, err = catalog.Available(t.Context())
	if err != nil || len(available) != 1 || available[0].Name != "Reason" || len(available[0].ThinkingLevels) < 2 {
		t.Fatalf("models: %#v %v", available, err)
	}
	if _, err = catalog.Configure(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	available, err = catalog.Available(t.Context())
	if err != nil || len(available) != 0 {
		t.Fatalf("stale models: %#v %v", available, err)
	}
}
func TestUserFakeProviderStreamsWithoutCredentials(t *testing.T) {
	catalog := NewCatalog(true)
	defer catalog.Models.Close()
	for _, id := range []string{"echo", "shout", "think", "context"} {
		model := catalog.Models.GetModel("faux", id)
		result := catalog.Models.Complete(t.Context(), model, ai.Context{Messages: []ai.Message{ai.UserMessage{Content: ai.UserText("hello")}}})
		if result.ErrorMessage != "" || !strings.Contains(ai.ContentText(result.Content), "hello") {
			t.Fatalf("%s: %#v", id, result)
		}
	}
}
func TestUserDiscoveryFailureDoesNotExposeSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "SECRET", http.StatusServiceUnavailable) }))
	defer server.Close()
	catalog := NewCatalog(false)
	defer catalog.Models.Close()
	failures, err := catalog.Configure(t.Context(), []ProviderSettings{{ID: "local", API: "openai-responses", BaseURL: server.URL + "/SECRET", APIKey: "SECRET"}})
	if err != nil || !strings.Contains(failures["local"], "HTTP 503") || strings.Contains(failures["local"], "SECRET") {
		t.Fatalf("failure: %v %v", failures, err)
	}
}

func TestUserCustomAPIsUseTheirDiscoveryContracts(t *testing.T) {
	for _, api := range []string{"openai-completions", "openai-responses", "anthropic-messages"} {
		t.Run(api, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wantPath := "/v1/models"
				if api == "anthropic-messages" {
					if r.Header.Get("x-api-key") != "test" || r.Header.Get("anthropic-version") == "" {
						t.Error("missing Anthropic headers")
					}
				} else if r.Header.Get("Authorization") != "Bearer test" {
					t.Error("missing OpenAI header")
				}
				if r.URL.Path != wantPath {
					t.Errorf("discovery path %q", r.URL.Path)
				}
				_, _ = w.Write([]byte(`{"data":[{"id":"chat"}]}`))
			}))
			defer server.Close()
			base := server.URL
			if api != "anthropic-messages" {
				base += "/v1"
			}
			catalog := NewCatalog(false)
			defer catalog.Models.Close()
			failures, err := catalog.Configure(t.Context(), []ProviderSettings{{ID: "custom", API: api, BaseURL: base, APIKey: "test"}})
			if err != nil || len(failures) > 0 {
				t.Fatalf("configure: %v %v", failures, err)
			}
			model := catalog.Models.GetModel("custom", "chat")
			if model == nil || string(model.ProviderMeta.API) != api {
				t.Fatalf("discovered model: %#v", model)
			}
		})
	}
}

// Observe the request deadline, then send the request through the real HTTP transport.
type discoveryDeadlineTransport struct {
	http.RoundTripper
	requests chan *http.Request
}

func (t discoveryDeadlineTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.requests <- request
	return t.RoundTripper.RoundTrip(request)
}
func TestUserModelDiscoveryHasABoundedRequestAndCanBeCanceled(t *testing.T) {
	// Given a provider that accepts a request but never sends a response.
	started, stopped := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	requests := make(chan *http.Request, 1)
	previousClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: discoveryDeadlineTransport{RoundTripper: server.Client().Transport, requests: requests}}
	catalog := NewCatalog(false)
	ctx, cancel := context.WithCancel(t.Context())
	defer func() { cancel(); catalog.Models.Close(); server.Close(); http.DefaultClient = previousClient }()
	finished := make(chan error, 1)
	before := time.Now()
	go func() {
		_, err := catalog.Configure(ctx, []ProviderSettings{{ID: "unresponsive", API: "openai-completions", BaseURL: server.URL + "/private-url", APIKey: "private-key"}})
		finished <- err
	}()
	var request *http.Request
	select {
	case request = <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery request did not start")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not receive discovery request")
	}
	// The native request has a 30-second bound even when the caller has no deadline.
	deadline, ok := request.Context().Deadline()
	if !ok || deadline.Before(before.Add(30*time.Second)) || deadline.After(time.Now().Add(30*time.Second)) {
		t.Fatal("model discovery request has no 30-second deadline")
	}
	// When the caller cancels, both the catalog operation and real HTTP request stop.
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled discovery: %v", err)
		}
		if strings.Contains(err.Error(), "private-key") || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "private-url") {
			t.Fatal("discovery error exposed credentials or URL")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("catalog did not stop after cancellation")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP request did not stop after cancellation")
	}
	if catalog.Models.GetProvider("unresponsive") == nil {
		t.Fatal("failed discovery removed the configured provider")
	}
}
