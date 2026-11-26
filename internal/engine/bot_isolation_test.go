package engine

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/mcptest"
)

func heldBotMCP(t *testing.T, config *Config) (<-chan struct{}, func(), *atomic.Int32) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var once, released sync.Once
	starts := new(atomic.Int32)
	mcp := &mcptest.Server{Dir: t.TempDir()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var request struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(body, &request) == nil && request.Method == "initialize" {
			starts.Add(1)
			once.Do(func() { close(entered) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		mcp.ServeHTTP(w, r)
	}))
	unblock := func() { released.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); server.Close() })
	if config.MCPServers == nil {
		config.MCPServers = testMCPStore(t, config.DataDir)
	}
	testSetMCP(t, config.MCPServers, "alpha", "held", serverConfig{Type: "http", URL: server.URL})
	return entered, unblock, starts
}

func lifecycleResult(t *testing.T, result <-chan error, label string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not complete while alpha MCP was held", label)
		return nil
	}
}

func TestUserHeldBotMCPDoesNotBlockOtherBot(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cold_open", "reload"} {
		t.Run(mode, func(t *testing.T) {
			// Given a warm beta and alpha held at a real MCP initialization request.
			config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
			config.MCPServers = testMCPStore(t, config.DataDir)
			a := coreEngine(t, config)
			beta := coreCreate(t, a, "beta", coreOptions("echo"))
			if mode == "reload" {
				coreCreate(t, a, "alpha", coreOptions("echo"))
			}
			entered, release, starts := heldBotMCP(t, &config)
			opened := make(chan error, 1)
			go func() {
				if mode == "reload" {
					opened <- a.Reload(t.Context(), "alpha")
				} else {
					_, err := a.Create(t.Context(), "alpha", coreOptions("echo"))
					opened <- err
				}
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("alpha did not reach MCP initialization")
			}
			// When health, history, and new beta work run before releasing alpha.
			for _, operation := range []struct {
				name string
				run  func() error
			}{
				{"health", func() error { return a.Health(t.Context()) }},
				{"beta history", func() error { _, err := a.History(t.Context(), beta, 1<<60, 100); return err }},
				{"beta submit and settlement", func() error {
					s, err := a.Submit(t.Context(), beta, Input{RequestID: "beta", Content: "responsive", Kind: "message", Options: coreOptions("echo")})
					if err != nil {
						return err
					}
					outcome, err := s.Wait(t.Context())
					if err == nil && (outcome.Status != "done" || outcome.Answer != "echo: responsive") {
						return errors.New("beta execution did not finish")
					}
					return err
				}},
			} {
				result := make(chan error, 1)
				go func() { result <- operation.run() }()
				if err := lifecycleResult(t, result, operation.name); err != nil {
					t.Fatalf("%s: %v", operation.name, err)
				}
			}
			// Reload waits only for its own bot: gamma reloads, and alpha reports busy.
			if err := a.Reload(t.Context(), "gamma"); err != nil {
				t.Fatalf("Reload of gamma waited for alpha: %v", err)
			}
			if err := a.Reload(t.Context(), "alpha"); !errors.Is(err, ErrBusy) {
				t.Fatalf("Reload ignored the open of alpha: %v", err)
			}
			// Then alpha connects exactly once and completes after the gate opens.
			release()
			if err := lifecycleResult(t, opened, "alpha open"); err != nil {
				t.Fatal(err)
			}
			if starts.Load() != 1 {
				t.Fatalf("MCP starts: %d", starts.Load())
			}
		})
	}
}

func TestUserLateStopDuringBotOpenIsolatesBeforeResume(t *testing.T) {
	t.Parallel()
	// Given stored running and queued work whose next tool start is observable.
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	first := coreEngine(t, config)
	ref := coreCreate(t, first, "alpha", coreOptions("bash"))
	workspace := filepath.Join(config.DataDir, "agents", "alpha", "workspace")
	coreSubmit(t, first, ref, "running", "echo start >> starts; touch started; while :; do sleep 1; done", "bash")
	coreFile(t, filepath.Join(workspace, "started"))
	input := Input{RequestID: "queued", Content: "withdraw before resume", Kind: "message", Options: coreOptions("echo")}
	if _, err := first.Submit(t.Context(), ref, input); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	entered, release, _ := heldBotMCP(t, &config)
	a := coreEngine(t, config)
	opened := make(chan error, 1)
	go func() { _, err := a.History(t.Context(), ref, 1<<60, 100); opened <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("alpha did not reach MCP initialization")
	}
	// When Stop records its intent after opening starts, its caller may cancel waiting.
	if err := a.Recover(t.Context(), []Ref{ref}); err == nil {
		t.Fatal("Recover accepted references after opening started")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- a.StopSession(ctx, ref) }()
	if err := lifecycleResult(t, stopped, "late cancelled Stop"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Stop: %v", err)
	}
	release()
	if err := lifecycleResult(t, opened, "recovered history"); err != nil {
		t.Fatal(err)
	}
	// Then withdrawal is passive and durable before any stored tool can resume.
	history, err := a.History(t.Context(), ref, 1<<60, 100)
	if err != nil {
		t.Fatal(err)
	}
	assertWithdrawnInput(t, history.Items, input)
	deadline, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	for {
		err := a.CheckIdle(deadline, ref)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("Stop settlement: %v", err)
		}
		select {
		case <-deadline.Done():
			t.Fatalf("Stop left native work running: %v", err)
		case <-time.After(5 * time.Millisecond):
		}
	}
	actions, err := os.ReadFile(filepath.Join(workspace, "starts"))
	if err != nil || string(actions) != "start\n" {
		t.Fatalf("Stop resumed stored tool: %q, %v", actions, err)
	}
}

func TestUserCancelledSubmissionFollowerStopsWaitingForSameRef(t *testing.T) {
	t.Parallel()
	// Given one saved conversation and a first submission held during bot opening.
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	first := coreEngine(t, config)
	ref := coreCreate(t, first, "alpha", coreOptions("echo"))
	if err := first.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	entered, release, starts := heldBotMCP(t, &config)
	a := coreEngine(t, config)
	owner := make(chan error, 1)
	go func() {
		s, err := a.Submit(t.Context(), ref, Input{RequestID: "owner", Content: "owner", Kind: "message", Options: coreOptions("echo")})
		if err == nil {
			_, err = s.Wait(t.Context())
		}
		owner <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("alpha did not reach MCP initialization")
	}
	// When a follower cancels at the same conversation's native admission boundary.
	ctx, cancel := context.WithCancel(t.Context())
	follower := make(chan error, 1)
	go func() {
		_, err := a.Submit(ctx, ref, Input{RequestID: "follower", Content: "cancelled", Kind: "message", Options: coreOptions("echo")})
		follower <- err
	}()
	cancel()
	if err := lifecycleResult(t, follower, "same-ref follower"); !errors.Is(err, context.Canceled) {
		t.Fatalf("follower cancellation: %v", err)
	}
	// Then the owner completes and exactly one MCP connection exists.
	release()
	if err := lifecycleResult(t, owner, "owner submission"); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 {
		t.Fatalf("MCP starts: %d", starts.Load())
	}
}

func TestUserCancelledBotOpenFollowerStopsWaiting(t *testing.T) {
	t.Parallel()
	// Given alpha is opening exactly one real MCP connection.
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	entered, release, starts := heldBotMCP(t, &config)
	a := coreEngine(t, config)
	owner := make(chan error, 1)
	go func() { _, err := a.Create(t.Context(), "alpha", coreOptions("echo")); owner <- err }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("alpha did not reach MCP initialization")
	}
	// When another caller cancels while waiting for the same bot.
	ctx, cancel := context.WithCancel(t.Context())
	follower := make(chan error, 1)
	go func() { _, err := a.Create(ctx, "alpha", coreOptions("echo")); follower <- err }()
	cancel()
	if err := lifecycleResult(t, follower, "cancelled follower"); !errors.Is(err, context.Canceled) {
		t.Fatalf("follower cancellation: %v", err)
	}
	// Then cancellation leaves the owner intact and no second connection starts.
	release()
	if err := lifecycleResult(t, owner, "opening owner"); err != nil {
		t.Fatal(err)
	}
	coreCreate(t, a, "alpha", coreOptions("echo"))
	if starts.Load() != 1 {
		t.Fatalf("MCP starts: %d", starts.Load())
	}
}

func TestUserCloseCancelsHeldBotOpenAndReload(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cold_open", "reload"} {
		t.Run(mode, func(t *testing.T) {
			// Given alpha is held during a real MCP connection.
			config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
			config.MCPServers = testMCPStore(t, config.DataDir)
			a := coreEngine(t, config)
			if mode == "reload" {
				coreCreate(t, a, "alpha", coreOptions("echo"))
			}
			entered, _, _ := heldBotMCP(t, &config)
			opened := make(chan error, 1)
			go func() {
				if mode == "reload" {
					opened <- a.Reload(t.Context(), "alpha")
				} else {
					_, err := a.Create(t.Context(), "alpha", coreOptions("echo"))
					opened <- err
				}
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("alpha did not reach MCP initialization")
			}
			// When shutdown cancels and joins the open without releasing the MCP gate.
			closed := make(chan error, 1)
			go func() { closed <- a.Close(t.Context()) }()
			if err := lifecycleResult(t, closed, "Close"); err != nil {
				t.Fatal(err)
			}
			if err := lifecycleResult(t, opened, "cancelled open"); !errors.Is(err, ErrClosed) && !errors.Is(err, context.Canceled) {
				t.Fatalf("open after Close: %v", err)
			}
			// Then no caller can acquire a resource after shutdown.
			if _, err := a.Create(t.Context(), "alpha", coreOptions("echo")); !errors.Is(err, ErrClosed) {
				t.Fatalf("Create after Close: %v", err)
			}
		})
	}
}
