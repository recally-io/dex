package app_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUserClosesTimelineWithoutServerErrors(t *testing.T) {
	// Given a real event stream and captured application and HTTP server logs.
	logPath := filepath.Join(t.TempDir(), "server.log")
	logs, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logs.Close() })
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prior) })
	d := startDex(t, t.TempDir(), "")
	chat := d.newSession(t)
	path := fmt.Sprintf("/api/agents/default/chats/%d/events", chat.ID)
	done := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.app.Handler().ServeHTTP(w, r)
		close(done)
	}))
	srv.Config.ErrorLog = log.New(logs, "http: ", 0)
	srv.Start()
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("timeline: HTTP %d, content type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() || scanner.Text() != "event: snapshot" {
		t.Fatalf("initial event: %q, error: %v", scanner.Text(), scanner.Err())
	}

	// When the user closes the timeline after the first event.
	cancel()
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeline handler did not stop after cancellation")
	}

	// Then cancellation ends the stream without an API error or a second status.
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "context canceled") || strings.Contains(string(got), "superfluous response.WriteHeader") {
		t.Fatalf("closing a timeline produced server errors:\n%s", got)
	}
	t.Log("artifact: real HTTP timeline closed without cancellation or duplicate-header errors")
}

func TestUserTimelineFailureDoesNotAppendJSON(t *testing.T) {
	t.Parallel()
	// Given a saved conversation whose engine cannot open until discovery finishes.
	d, _, _, chatID := startWithHeldDiscovery(t, false)

	// When the event reader fails after its SSE headers have been sent.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("%s/api/agents/default/chats/%d/events", d.srv.URL, chatID), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := d.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	// Then the stream ends for reconnect, without a JSON error in the SSE body.
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" || len(body) != 0 {
		t.Fatalf("failed timeline: HTTP %d, content type %q, body %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	// An error before the response starts still returns its normal JSON status.
	d.must(t, http.MethodGet, "/api/agents/default/chats/999999/events", nil, nil, http.StatusNotFound)
	t.Log("artifact: failed SSE ended without JSON; unknown timeline still returned HTTP 404")
}
