package app_test

import (
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"
)

// fakeTelegram is a Bot API server. Telegram is a system boundary, so the tests use a fake instead
// of the real service. getUpdates returns every queued update with an ID at or above the offset,
// as Telegram does.
type fakeTelegram struct {
	srv  *httptest.Server
	sent chan sentMessage

	mu      sync.Mutex
	updates []map[string]any
}

type sentMessage struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

const fakeToken = "123:secret"

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{sent: make(chan sentMessage, 16)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// message queues a text message from user in chat. Telegram delivers an update again until the bot
// confirms it, so a test can queue one updateID twice.
func (f *fakeTelegram) message(updateID, user, chat int64, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, map[string]any{
		"update_id": updateID,
		"message": map[string]any{
			"message_id": updateID, "from": map[string]any{"id": user}, "chat": map[string]any{"id": chat},
			"text": text,
		},
	})
}

func (f *fakeTelegram) serve(w http.ResponseWriter, r *http.Request) {
	method, ok := strings.CutPrefix(r.URL.Path, "/bot"+fakeToken+"/")
	if !ok {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	var result any = true
	switch method {
	case "getUpdates":
		var params struct {
			Offset int64 `json:"offset"`
		}
		_ = json.UnmarshalRead(r.Body, &params)
		f.mu.Lock()
		var updates []map[string]any
		for _, u := range f.updates {
			if u["update_id"].(int64) >= params.Offset {
				updates = append(updates, u)
			}
		}
		f.mu.Unlock()
		if len(updates) == 0 {
			// A short long poll, so the bot does not spin.
			select {
			case <-r.Context().Done():
			case <-time.After(100 * time.Millisecond):
			}
		}
		result = updates
	case "sendMessage":
		var m sentMessage
		_ = json.UnmarshalRead(r.Body, &m)
		f.sent <- m
	}
	_ = json.MarshalWrite(w, map[string]any{"ok": true, "result": result})
}

// next returns the next message the bot sent, or fails after 30 s.
func (f *fakeTelegram) next(t *testing.T) sentMessage {
	t.Helper()
	select {
	case m := <-f.sent:
		return m
	case <-time.After(30 * time.Second):
		t.Fatal("the bot sent no message in 30 s")
		return sentMessage{}
	}
}

// startTelegram starts dex with the echo model, and turns Telegram on in the settings.
func startTelegram(t *testing.T, f *fakeTelegram, dataDir string, allowed ...int64) *dex {
	t.Helper()
	d := startDex(t, dataDir, f.srv.URL)
	d.setModel(t, "echo")
	d.must(t, http.MethodPut, "/api/settings/telegram", map[string]any{"token": fakeToken, "allowedUsers": allowed}, nil)
	return d
}

func TestUserGetsAnswerInTelegram(t *testing.T) {
	// Given dex with a Telegram bot that user 42 can use
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)

	// When user 42 sends a message in chat 7
	f.message(1, 42, 7, "hi")

	// Then the bot sends the agent's answer to chat 7
	if got := f.next(t); got != (sentMessage{ChatID: 7, Text: "echo: hi"}) {
		t.Fatalf("sent = %+v", got)
	}

	// And the web UI shows the chat, but cannot write to it
	chats := d.chats(t)
	if len(chats) != 1 || chats[0].Channel != "telegram" || chats[0].Title != "hi" {
		t.Fatalf("chats = %+v", chats)
	}
	d.watch(t, chats[0].ID).until(t, "answer", answered("echo: hi"))
	status, raw := d.call(t, http.MethodPost, fmt.Sprintf("/api/chats/%d/messages", chats[0].ID),
		map[string]string{"id": uuid.NewV7().String(), "text": "from the web"})
	if status != http.StatusForbidden {
		t.Fatalf("web message to a Telegram chat: %d %s", status, raw)
	}
}

func TestTelegramIgnoresUsersWhoAreNotAllowed(t *testing.T) {
	// Given dex with a Telegram bot that only user 42 can use
	f := newFakeTelegram(t)
	dataDir := t.TempDir()
	startTelegram(t, f, dataDir, 42)

	// When user 99 sends a message in chat 8, and then user 42 sends one in chat 7
	f.message(1, 99, 8, "let me in")
	f.message(2, 42, 7, "ok")

	// Then only user 42 gets an answer, and dex stores nothing for chat 8. The bot handles updates
	// in order, so it handled the first one before the answer to the second.
	if got := f.next(t); got != (sentMessage{ChatID: 7, Text: "echo: ok"}) {
		t.Fatalf("sent = %+v", got)
	}
	conn, err := sql.Open("sqlite", filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	var n int
	err = conn.QueryRowContext(t.Context(),
		"SELECT count(*) FROM conversations WHERE channel_key = 'telegram:8'").Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("dex stored %d conversations for chat 8", n)
	}
}

func TestTelegramMessageDeliveredTwiceIsAnsweredOnce(t *testing.T) {
	// Given dex with a Telegram bot that user 42 can use
	f := newFakeTelegram(t)
	startTelegram(t, f, t.TempDir(), 42)

	// When Telegram delivers one message twice, as after a restart before the bot confirmed it,
	// and then a second message
	f.message(1, 42, 7, "hi")
	f.message(1, 42, 7, "hi")
	f.message(2, 42, 7, "after")

	// Then the agent answers the first message once, then the second message
	got := []string{f.next(t).Text, f.next(t).Text}
	if !slices.Equal(got, []string{"echo: hi", "echo: after"}) {
		t.Fatalf("sent = %q", got)
	}
}
