package app_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
)

// Restart with a saved provider whose real HTTP discovery request is held at the boundary.
func startWithHeldDiscovery(t *testing.T, fail bool) (*dex, func(), <-chan struct{}, int64) {
	t.Helper()
	entered, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	var hold atomic.Bool
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, `data: {"id":"test","object":"chat.completion.chunk","created":0,"model":"remote","choices":[{"index":0,"delta":{"role":"assistant","content":"remote answer"},"finish_reason":null}]}

data: {"id":"test","object":"chat.completion.chunk","created":0,"model":"remote","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`)
			return
		}
		if hold.Load() {
			close(entered)
			defer close(stopped)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			if fail {
				http.Error(w, "SECRET remote failure", http.StatusServiceUnavailable)
				return
			}
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"remote"}]}`))
	}))
	t.Cleanup(endpoint.Close)
	t.Cleanup(unblock)
	data, home := t.TempDir(), t.TempDir()
	first := startDexHome(t, app.Config{DataDir: data}, home)
	first.setModel(t, "echo")
	first.must(t, http.MethodPut, "/api/settings/providers/lan", map[string]any{"api": "openai-completions", "baseUrl": endpoint.URL, "apiKey": "SECRET"}, nil)
	first.must(t, http.MethodPut, "/api/settings/providers/fixed", map[string]any{"api": "openai-completions", "baseUrl": endpoint.URL, "models": []string{"static"}}, nil)
	first.must(t, http.MethodPut, "/api/settings/providers/openai", map[string]any{"apiKey": "SECRET"}, nil)
	first.must(t, http.MethodPut, "/api/agents/default", map[string]string{"name": "Dex", "provider": "lan", "modelId": "remote", "thinkingLevel": "off"}, nil)
	savedChat := first.newChat(t, "before restart")
	first.watch(t, savedChat).until(t, "saved native dynamic conversation", answered("remote answer"))
	first.stop()
	hold.Store(true)
	cfg := app.Config{DataDir: data, Home: home, ToolEnv: map[string]string{"PATH": os.Getenv("PATH"), "HOME": home}, FakeModel: true}
	type result struct {
		app *app.App
		err error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { a, err := app.Start(ctx, cfg); done <- result{a, err} }()
	running := true
	t.Cleanup(func() {
		unblock()
		if running {
			r := <-done
			if r.app != nil {
				_ = r.app.Close()
			}
		}
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("saved discovery did not start")
	}
	// This deadline starts after the request enters the endpoint; it does not depend on 30s.
	select {
	case r := <-done:
		running = false
		if r.err != nil {
			t.Fatal(r.err)
		}
		d := &dex{app: r.app, srv: httptest.NewServer(r.app.Handler())}
		d.srv.Client().Timeout = time.Second
		t.Cleanup(d.stop)
		t.Log("artifact: Start returned while saved discovery remained held")
		return d, unblock, stopped, savedChat
	case <-time.After(time.Second):
		t.Fatal("Start blocked on saved model discovery before HTTP handler existed")
	}
	return nil, nil, nil, 0
}

func TestUserStartupServesHTTPAndRetainsPendingAdmissionDuringDiscovery(t *testing.T) {
	t.Parallel()
	// Given saved dynamic, static, and builtin providers, with discovery held on restart.
	d, release, _, savedChat := startWithHeldDiscovery(t, false)
	// Then health and Settings respond before discovery finishes.
	var health struct {
		RuntimeReady bool `json:"runtimeReady"`
	}
	d.must(t, http.MethodGet, "/api/health", nil, &health)
	if !health.RuntimeReady {
		t.Fatal("HTTP readiness waited for discovery")
	}
	d.must(t, http.MethodGet, "/api/settings", nil, nil)
	models := d.models(t)
	if !slices.Contains(models, "fixed/static") || !slices.Contains(models, "faux/echo") || !slices.ContainsFunc(models, func(m string) bool { return strings.HasPrefix(m, "openai/") }) {
		t.Fatalf("saved static models or builtin credentials were not registered: %v", models)
	}
	// Cold runtime operations return retryable 503 without changing saved settings.
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d/history?before=9007199254740991&limit=100", savedChat), nil, nil, http.StatusServiceUnavailable)
	d.must(t, http.MethodPost, "/api/agents/default/extensions/reload", nil, nil, http.StatusServiceUnavailable)
	d.must(t, http.MethodPut, "/api/agents/default", map[string]string{"name": "Must not save", "provider": "lan", "modelId": "remote", "thinkingLevel": "off"}, nil, http.StatusServiceUnavailable)
	var saved struct {
		Name     string `json:"name"`
		Provider string `json:"provider"`
		ModelID  string `json:"modelId"`
	}
	d.must(t, http.MethodGet, "/api/agents/default", nil, &saved)
	if saved.Name != "Dex" || saved.Provider != "lan" || saved.ModelID != "remote" {
		t.Fatalf("pending validation changed saved agent: %+v", saved)
	}
	// When input arrives before the cold harness can safely resume, its receipt stays pending.
	id := d.send(t, savedChat, "after discovery")
	var status string
	var admitted any
	if err := sessionDB(t, d).QueryRowContext(t.Context(), "SELECT status, admitted_at FROM submissions WHERE id=?", id).Scan(&status, &admitted); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || admitted != nil {
		t.Fatalf("pending receipt lost: status=%s admitted=%v", status, admitted)
	}
	t.Logf("artifact: healthy HTTP, Settings and static models; receipt %s stays pending", id)
	// Then discovery publication wakes reconciliation and the same receipt completes.
	release()
	eventually(t, "discovered model published", func() (bool, any) { got := d.models(t); return slices.Contains(got, "lan/remote"), got })
	d.watch(t, savedChat).until(t, "pending dynamic input recovered", func(tr *transcript) bool {
		count := 0
		for _, item := range tr.items {
			if item.Kind == "assistant" && item.Text == "remote answer" {
				count++
			}
		}
		return count == 2
	})
	eventually(t, "receipt settled", func() (bool, any) {
		err := sessionDB(t, d).QueryRowContext(t.Context(), "SELECT status FROM submissions WHERE id=?", id).Scan(&status)
		return err == nil && status == "done", status
	})
}

func TestUserStartupDiscoveryFailureAppearsInSettings(t *testing.T) {
	t.Parallel()
	// Given a saved endpoint whose response is held while HTTP starts.
	d, release, _, _ := startWithHeldDiscovery(t, true)
	d.must(t, http.MethodGet, "/api/settings", nil, nil)
	// When discovery fails, Settings eventually reports a sanitized provider error.
	release()
	eventually(t, "provider discovery error", func() (bool, any) {
		status, body := d.call(t, http.MethodGet, "/api/settings", nil)
		if strings.Contains(body, "SECRET") {
			t.Fatal("settings exposed discovery secret")
		}
		return status == http.StatusOK && strings.Contains(body, "model discovery HTTP 503"), body
	})
}

func TestUserAppCloseCancelsHeldStartupDiscovery(t *testing.T) {
	t.Parallel()
	// Given an app that serves HTTP while its saved discovery request remains held.
	d, _, stopped, _ := startWithHeldDiscovery(t, false)
	// When the user stops the app, Close must cancel and join the request promptly.
	done := make(chan struct{})
	go func() { d.stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("app.Close waited for network discovery timeout")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("discovery HTTP request survived app.Close")
	}
	t.Log("artifact: app.Close joined held discovery without its 30s timeout")
}

func TestUserCancelledProviderSaveContinuesDiscoveryAndAllowsOtherSaves(t *testing.T) {
	t.Parallel()
	// Given a real HTTP provider save that waits on discovery.
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"remote"}]}`))
	}))
	defer endpoint.Close()
	defer unblock()
	d := startDex(t, t.TempDir(), "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, d.srv.URL+"/api/settings/providers/lan", strings.NewReader(`{"api":"openai-completions","baseUrl":"`+endpoint.URL+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	result := make(chan error, 1)
	go func() {
		response, err := d.srv.Client().Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider save did not start discovery")
	}
	// When the client stops waiting, the saved provider continues discovery.
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled request returned no error")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled PUT remained blocked")
	}
	d.srv.Client().Timeout = time.Second
	d.must(t, http.MethodPut, "/api/settings/providers/static", map[string]any{"api": "openai-completions", "baseUrl": endpoint.URL, "models": []string{"fixed"}}, nil)
	d.must(t, http.MethodDelete, "/api/settings/providers/static", nil, nil)
	// Then the original discovery still publishes models after its client has gone.
	unblock()
	eventually(t, "cancelled PUT discovery published", func() (bool, any) { models := d.models(t); return slices.Contains(models, "lan/remote"), models })
	t.Log("artifact: cancelled PUT left saved discovery alive; unrelated save/delete stayed responsive")
}

func TestUserResaveRetriesFailedDiscoveryAndKeepsErrorsUntilRecovery(t *testing.T) {
	t.Parallel()
	// Given a saved provider whose discovery fails at a real HTTP boundary.
	var healthy atomic.Bool
	var requests atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if !healthy.Load() {
			http.Error(w, "PRIVATE", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"recovered"}]}`))
	}))
	defer endpoint.Close()
	d := startDex(t, t.TempDir(), "")
	input := map[string]any{"api": "openai-completions", "baseUrl": endpoint.URL}
	var saved struct {
		Error      string `json:"error"`
		ModelCount int    `json:"modelCount"`
	}
	for attempt := range 2 {
		// When the endpoint still fails, saving the same settings retries and reports the failure.
		d.must(t, http.MethodPut, "/api/settings/providers/lan", input, &saved)
		if !strings.Contains(saved.Error, "HTTP 500") || saved.ModelCount != 0 {
			t.Fatalf("failed save %d hid discovery error: %+v", attempt+1, saved)
		}
		if got := requests.Load(); got != int32(attempt+1) {
			t.Fatalf("resave did not retry failed discovery: requests=%d want=%d", got, attempt+1)
		}
	}
	// Then the same saved settings discover models after the endpoint recovers.
	healthy.Store(true)
	saved.Error = ""
	d.must(t, http.MethodPut, "/api/settings/providers/lan", input, &saved)
	if saved.Error != "" || saved.ModelCount != 1 || requests.Load() != 3 {
		t.Fatalf("resave did not recover: %+v requests=%d", saved, requests.Load())
	}
	// A healthy catalog stays usable without another discovery on a no-op save.
	d.must(t, http.MethodPut, "/api/settings/providers/lan", input, &saved)
	if saved.Error != "" || saved.ModelCount != 1 || requests.Load() != 3 {
		t.Fatalf("healthy catalog refreshed unnecessarily: %+v requests=%d", saved, requests.Load())
	}
	t.Log("artifact: failed resaves kept HTTP 500; healthy resave recovered one model; healthy catalog reused")
}
