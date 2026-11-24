package app_test

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/mcptest"
)

func TestUserTelegramPollAdmitsTopicWhileMainAdmissionWaits(t *testing.T) {
	t.Parallel()
	// Given private Main needs a cold MCP connection, which the topic does not use.
	entered, release := make(chan struct{}), make(chan struct{})
	enter := sync.OnceFunc(func() { close(entered) })
	unblock := sync.OnceFunc(func() { close(release) })
	boundary := &mcptest.Server{Dir: t.TempDir()}
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enter()
		select {
		case <-r.Context().Done():
		case <-release:
			boundary.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(mcp.Close)
	f := newFakeTelegram(t)
	dir := t.TempDir()
	startTelegram(t, f, dir, 42)
	defer unblock()
	conn := controlWriteDB(t, dir)
	config, err := json.Marshal(map[string]string{"type": "http", "url": mcp.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO mcp_servers (agent_id, name, config, position, updated_at) VALUES ('default','slow',?,0,0)`, string(config)); err != nil {
		t.Fatal(err)
	}
	f.message(1, 42, 42, "private")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("private admission did not reach MCP")
	}
	if n := count(t, conn, `SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND admitted_at IS NULL`); n != 1 {
		t.Fatalf("expected one source receipt without native admission, got %d", n)
	}

	// When Telegram sends another update, the poll advances and the topic answers first.
	f.message(1, 42, 42, "private")
	f.groupText(2, 42, -100, 7, "topic")
	select {
	case got := <-f.sent:
		if got.ChatID != -100 || got.TopicID != 7 || got.Text != "echo: topic" {
			t.Fatalf("topic should answer first: %+v", got)
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("topic was held by private admission; poll offset %d", f.pollOffset.Load())
	}

	// Then Main admits the original source exactly once after MCP is available.
	unblock()
	if got := f.next(t); got.ChatID != 42 || got.Text != "echo: private" {
		t.Fatalf("private reply: %+v", got)
	}
	if n := count(t, conn, `SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND admitted_at IS NOT NULL`); n != 1 {
		t.Fatalf("expected one admitted source receipt, got %d", n)
	}
}
