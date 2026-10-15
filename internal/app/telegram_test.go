package app_test

import (
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"
	"uuid"

	"github.com/recally-io/dex/internal/settings"
)

// fakeTelegram is a Bot API server. Telegram is a system boundary, so the tests use a fake instead
// of the real service. getUpdates returns every queued update with an ID at or above the offset,
// as Telegram does.
type fakeTelegram struct {
	fileGate      <-chan struct{}
	draftGate     <-chan struct{}
	permanentChat atomic.Int64
	failAfter     atomic.Int64
	sendCount     atomic.Int64
	failSend      atomic.Bool
	attempted     chan struct{}
	srv           *httptest.Server
	sent          chan sentMessage

	mu          sync.Mutex
	updates     []map[string]any
	rejectHTML  bool
	rejectDraft bool
	failures    map[string][]platformFailure
	files       map[string]string
	events      chan platformEvent
	nextID      int64
}

type platformFailure struct{ Code, Retry int }
type platformEvent struct {
	Method    string
	Text      string
	MessageID int64
}

type sentMessage struct {
	ChatID    int64  `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

const fakeToken = "123:secret"

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{sent: make(chan sentMessage, 64), attempted: make(chan struct{}, 64), failures: map[string][]platformFailure{}, files: map[string]string{}, events: make(chan platformEvent, 512)}
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
			"message_id": updateID, "from": map[string]any{"id": user}, "chat": map[string]any{"id": chat, "type": "private"},
			"text": text,
		},
	})
}

func (f *fakeTelegram) serve(w http.ResponseWriter, r *http.Request) {
	if name, ok := strings.CutPrefix(r.URL.Path, "/file/bot"+fakeToken+"/"); ok {
		f.mu.Lock()
		body, exists := f.files[name]
		f.mu.Unlock()
		if !exists {
			http.NotFound(w, r)
			return
		}
		if f.fileGate != nil {
			f.events <- platformEvent{Method: "download", Text: name}
			select {
			case <-f.fileGate:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = w.Write([]byte(body))
		return
	}
	method, ok := strings.CutPrefix(r.URL.Path, "/bot"+fakeToken+"/")
	if !ok {
		http.Error(w, "bad token", http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	failures := f.failures[method]
	var failure platformFailure
	if len(failures) > 0 {
		failure = failures[0]
		f.failures[method] = failures[1:]
	}
	rejectHTML, rejectDraft := f.rejectHTML, f.rejectDraft
	f.mu.Unlock()
	if failure.Code != 0 {
		_ = json.MarshalWrite(w, map[string]any{"ok": false, "error_code": failure.Code, "description": "test platform failure", "parameters": map[string]any{"retry_after": failure.Retry}})
		return
	}
	if method == "sendMessageDraft" && f.draftGate != nil {
		f.events <- platformEvent{Method: "draftStarted"}
		select {
		case <-f.draftGate:
		case <-r.Context().Done():
			return
		}
	}
	var result any = true
	switch method {
	case "getMe":
		result = map[string]any{"id": 123, "username": "dex_test_bot"}
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
	case "sendMessage", "editMessageText", "sendMessageDraft", "sendChatAction", "setMyCommands":
		if method == "sendMessage" {
			select {
			case f.attempted <- struct{}{}:
			default:
			}
			n := f.sendCount.Add(1)
			if f.failSend.Load() || (f.failAfter.Load() > 0 && n > f.failAfter.Load()) {
				_ = json.MarshalWrite(w, map[string]any{"ok": false, "error_code": 500, "description": "temporary failure"})
				return
			}
		}
		var m struct {
			sentMessage
			MessageID int64 `json:"message_id"`
		}
		_ = json.UnmarshalRead(r.Body, &m)
		if f.permanentChat.Load() == m.ChatID && m.ChatID != 0 {
			_ = json.MarshalWrite(w, map[string]any{"ok": false, "error_code": 403, "description": "bot was blocked by the user"})
			return
		}
		if (rejectHTML && m.ParseMode == "HTML") || (rejectDraft && method == "sendMessageDraft") {
			_ = json.MarshalWrite(w, map[string]any{"ok": false, "error_code": 400, "description": "unsupported markup or draft"})
			return
		}
		f.mu.Lock()
		f.nextID++
		id := f.nextID
		f.mu.Unlock()
		if method == "sendMessage" || (method == "editMessageText" && m.ParseMode != "") {
			f.sent <- m.sentMessage
		}
		select {
		case f.events <- platformEvent{Method: method, Text: m.Text, MessageID: func() int64 {
			if method == "sendMessage" {
				return id
			}
			return m.MessageID
		}()}:
		default:
		}
		result = map[string]any{"message_id": id}
	case "getFile":
		var p struct {
			ID string `json:"file_id"`
		}
		_ = json.UnmarshalRead(r.Body, &p)
		f.mu.Lock()
		body, ok := f.files[p.ID]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		result = map[string]any{"file_path": p.ID, "file_size": len(body)}
		select {
		case f.events <- platformEvent{Method: method, Text: p.ID}:
		default:
		}
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
	if allowed == nil {
		allowed = []int64{}
	}
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
	if got := f.next(t); got != (sentMessage{ChatID: 7, Text: "echo: hi", ParseMode: "HTML"}) {
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
	// Telegram chats follow the agent's settings
	status, raw = d.call(t, http.MethodPut, fmt.Sprintf("/api/chats/%d/settings", chats[0].ID),
		map[string]string{"thinkingLevel": "high"})
	if status != http.StatusForbidden {
		t.Fatalf("settings of a Telegram chat: %d %s", status, raw)
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
	if got := f.next(t); got != (sentMessage{ChatID: 7, Text: "echo: ok", ParseMode: "HTML"}) {
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

func TestUserGetsFormattedTelegramAnswer(t *testing.T) {
	// Given a bot that user 42 can use.
	f := newFakeTelegram(t)
	startTelegram(t, f, t.TempDir(), 42)
	// When the answer contains Markdown and literal HTML characters.
	f.message(1, 42, 7, "**bold** & `<safe>`")
	// Then Telegram receives supported HTML with escaped literal text.
	got := f.next(t)
	if got.Text != "echo: <b>bold</b> &amp; <code>&lt;safe&gt;</code>" || got.ParseMode != "HTML" {
		t.Fatalf("formatted answer = %+v", got)
	}
}

func TestUserPairsTelegramOwnerOnce(t *testing.T) {
	// Given a saved bot without an owner.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir())
	var pair struct {
		URL string `json:"url"`
	}
	d.must(t, http.MethodPost, "/api/settings/telegram/pairing", nil, &pair)
	link, err := url.Parse(pair.URL)
	if err != nil {
		t.Fatal(err)
	}
	code := link.Query().Get("start")
	if code == "" {
		t.Fatalf("pairing link = %q", pair.URL)
	}
	// When an unpaired user sends a bad code, then the valid code.
	f.message(1, 99, 8, "/start wrong")
	if got := f.next(t); !strings.Contains(got.Text, "expired") {
		t.Fatalf("bad code reply = %+v", got)
	}
	f.message(2, 42, 7, "/start "+code)
	if got := f.next(t); !strings.Contains(got.Text, "paired") {
		t.Fatalf("pairing reply = %+v", got)
	}
	// Then the code cannot enroll a second user, and only the owner gets an answer.
	f.message(3, 99, 8, "/start "+code)
	if got := f.next(t); !strings.Contains(got.Text, "expired") {
		t.Fatalf("reused code reply = %+v", got)
	}
	f.message(4, 99, 8, "ignored")
	f.message(5, 42, 7, "hello")
	if got := f.next(t); got.Text != "echo: hello" || got.ChatID != 7 {
		t.Fatalf("owner reply = %+v", got)
	}
	if chats := d.chats(t); len(chats) != 1 {
		t.Fatalf("chats = %+v", chats)
	}
}

func TestUserGetsSplitTelegramCode(t *testing.T) {
	// Given a bot and a code block that cannot fit in one Telegram message.
	f := newFakeTelegram(t)
	startTelegram(t, f, t.TempDir(), 42)
	code := strings.Repeat("😀<&", 650)
	// When the model returns the code block.
	f.message(1, 42, 7, "```\n"+code+"\n```")
	// Then every part fits, and code tags close before the next message.
	var plain strings.Builder
	for plain.Len() < len("echo: \n\n"+code+"\n") {
		got := f.next(t)
		if len(utf16.Encode([]rune(got.Text))) > 4096 || got.ParseMode != "HTML" {
			t.Fatalf("invalid part: mode=%s units=%d", got.ParseMode, len(utf16.Encode([]rune(got.Text))))
		}
		if strings.Contains(got.Text, "<pre>") && !strings.HasSuffix(got.Text, "</code></pre>") {
			t.Fatalf("unclosed code block: %q", got.Text)
		}
		text := strings.NewReplacer("<pre><code>", "", "</code></pre>", "").Replace(got.Text)
		plain.WriteString(html.UnescapeString(text))
	}
	if !strings.Contains(plain.String(), code) {
		t.Fatalf("code was changed: length=%d", plain.Len())
	}
}

func TestUserGetsPlainTelegramAnswerWhenHTMLIsRejected(t *testing.T) {
	// Given a Bot API that rejects formatted messages.
	f := newFakeTelegram(t)
	f.rejectHTML = true
	startTelegram(t, f, t.TempDir(), 42)
	// When an answer contains Markdown.
	f.message(1, 42, 7, "**bold** & `<safe>` [link](https://example.com/?x=1&y=2)")
	// Then the bot sends the same text without markup.
	got := f.next(t)
	if got.Text != "echo: bold & <safe> link" || got.ParseMode != "" {
		t.Fatalf("plain fallback = %+v", got)
	}
}

func TestUserGetsTelegramAnswerAfterDurableFloodRetry(t *testing.T) {
	// Given a Bot API that rejects the first final send with 429.
	f := newFakeTelegram(t)
	f.failures["sendMessage"] = []platformFailure{{Code: 429, Retry: 1}}
	startTelegram(t, f, t.TempDir(), 42)
	// When the owner sends a message.
	f.message(1, 42, 7, "hi")
	// Then the shared durable sender retries and delivers the answer.
	if got := f.next(t); got.Text != "echo: hi" {
		t.Fatalf("retry answer = %+v", got)
	}
}

func (f *fakeTelegram) nextEvent(t *testing.T, method string) platformEvent {
	t.Helper()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case ev := <-f.events:
			if ev.Method == method {
				return ev
			}
		case <-deadline.C:
			t.Fatalf("no %s in 30 s", method)
			return platformEvent{}
		}
	}
}

func TestUserSeesTelegramDraft(t *testing.T) {
	// Given a bot and an answer that takes several seconds to produce.
	f := newFakeTelegram(t)
	startTelegram(t, f, t.TempDir(), 42)
	input := strings.Repeat("stream ", 400)
	// When the owner sends the message.
	f.message(1, 42, 7, input)
	// Then a draft appears before the complete final message.
	draft := f.nextEvent(t, "sendMessageDraft")
	if draft.Text == "" || !strings.HasPrefix("echo: "+input, draft.Text) {
		t.Fatalf("draft = %q", draft.Text)
	}
	if got := f.next(t); got.Text != strings.TrimSpace("echo: "+input) {
		t.Fatalf("final length = %d", len(got.Text))
	}
}

func TestUserSeesTelegramEditWhenDraftIsUnsupported(t *testing.T) {
	// Given an older Bot API without draft support.
	f := newFakeTelegram(t)
	f.rejectDraft = true
	startTelegram(t, f, t.TempDir(), 42)
	input := strings.Repeat("stream ", 400)
	// When the owner sends a message that streams for several seconds.
	f.message(1, 42, 7, input)
	// Then the bot sends one preview and updates that same message.
	preview := f.next(t)
	if preview.ParseMode != "" {
		t.Fatalf("preview = %+v", preview)
	}
	sent := f.nextEvent(t, "sendMessage")
	edit := f.nextEvent(t, "editMessageText")
	if edit.MessageID == 0 || edit.MessageID != sent.MessageID {
		t.Fatal("the preview edit has no message ID")
	}
	final := f.next(t)
	if final.ParseMode != "HTML" || final.Text != strings.TrimSpace("echo: "+input) {
		t.Fatalf("final = mode %q, length %d", final.ParseMode, len(final.Text))
	}
}

func TestUserSendsTelegramPhotoAndDocument(t *testing.T) {
	// Given a bot, two photo sizes, and a document.
	f := newFakeTelegram(t)
	f.files["small.png"] = string(pngImage(t))
	f.files["large.png"] = string(pngImage(t))
	f.files["notes.txt"] = "remember the milk\n"
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	// When the owner sends a captioned photo and a document, including a repeated update.
	f.mu.Lock()
	f.updates = append(f.updates, map[string]any{"update_id": int64(1), "message": map[string]any{
		"from": map[string]any{"id": 42}, "chat": map[string]any{"id": 7, "type": "private"}, "caption": "look",
		"photo": []map[string]any{{"file_id": "large.png", "width": 100, "height": 100}, {"file_id": "small.png", "width": 10, "height": 10}},
	}})
	document := map[string]any{"update_id": int64(2), "message": map[string]any{
		"from": map[string]any{"id": 42}, "chat": map[string]any{"id": 7, "type": "private"}, "caption": "read",
		"document": map[string]any{"file_id": "notes.txt", "file_name": "notes.txt"},
	}}
	f.updates = append(f.updates, document, document)
	f.mu.Unlock()
	// Then only the largest photo and one document are downloaded.
	if got := f.nextEvent(t, "getFile").Text; got != "large.png" {
		t.Fatalf("selected photo = %q", got)
	}
	if got := f.nextEvent(t, "getFile").Text; got != "notes.txt" {
		t.Fatalf("document = %q", got)
	}
	f.next(t)
	f.next(t)
	chats := d.chats(t)
	if len(chats) != 1 {
		t.Fatalf("chats = %+v", chats)
	}
	watch := d.watch(t, chats[0].ID)
	watch.until(t, "two attachment messages", func(tr *transcript) bool {
		count := 0
		for _, item := range tr.items {
			if item.Kind == "user" && len(item.Attachments) == 1 {
				count++
			}
		}
		return count == 2
	})
	var captions []string
	for _, item := range watch.tr.items {
		if item.Kind != "user" {
			continue
		}
		captions = append(captions, item.Text)
		file := item.Attachments[0]
		stored, err := os.ReadFile(filepath.Join(dir, "agents", "default", "workspace", file.Path))
		if err != nil || string(stored) != f.files[file.Name] {
			t.Fatalf("stored attachment %s: %v", file.Name, err)
		}
	}
	if !slices.Equal(captions, []string{"look", "read"}) {
		t.Fatalf("captions = %q", captions)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "agents", "default", "workspace", "uploads"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("uploads = %d, %v", len(entries), err)
	}
}

func TestUserCannotPairTelegramWithExpiredCode(t *testing.T) {
	// Given a bot with a pairing link that has expired in durable storage.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startTelegram(t, f, dir)
	var pair struct {
		URL string `json:"url"`
	}
	d.must(t, http.MethodPost, "/api/settings/telegram/pairing", nil, &pair)
	link, err := url.Parse(pair.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("sqlite", filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(t.Context(), "UPDATE telegram_settings SET pairing_expires=0"); err != nil {
		t.Fatal(err)
	}
	// When someone sends the expired code.
	f.message(1, 42, 7, "/start "+link.Query().Get("start"))
	// Then the bot refuses the code and stores no allowed user or conversation.
	if got := f.next(t); !strings.Contains(got.Text, "expired") {
		t.Fatalf("expired reply = %+v", got)
	}
	var users string
	if err = conn.QueryRowContext(t.Context(), "SELECT allowed_users FROM telegram_settings").Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != "" || len(d.chats(t)) != 0 {
		t.Fatalf("expired code stored user %q", users)
	}
}

func TestUserPairsTelegramAfterControlRestart(t *testing.T) {
	// Given a pending pairing link.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startTelegram(t, f, dir)
	var pair struct {
		URL string `json:"url"`
	}
	d.must(t, http.MethodPost, "/api/settings/telegram/pairing", nil, &pair)
	link, err := url.Parse(pair.URL)
	if err != nil {
		t.Fatal(err)
	}
	// When control restarts before the owner opens the link.
	d.stop()
	startDex(t, dir, f.srv.URL)
	f.message(1, 42, 7, "/start "+link.Query().Get("start"))
	// Then the link still pairs the owner, and the bot admits the owner's messages.
	if got := f.next(t); !strings.Contains(got.Text, "paired") {
		t.Fatalf("pairing after restart = %+v", got)
	}
	f.message(2, 42, 7, "after restart")
	if got := f.next(t); got.Text != "echo: after restart" {
		t.Fatalf("owner answer = %+v", got)
	}
}

func TestUserCannotSendOversizeTelegramDocument(t *testing.T) {
	// Given a bot that admits only owner 42.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	// When the owner sends a document larger than the Bot API limit.
	f.mu.Lock()
	f.updates = append(f.updates, map[string]any{"update_id": int64(1), "message": map[string]any{
		"from": map[string]any{"id": 42}, "chat": map[string]any{"id": 7, "type": "private"},
		"document": map[string]any{"file_id": "too-large", "file_name": "big.bin", "file_size": (20 << 20) + 1},
	}})
	f.mu.Unlock()
	// Then the bot explains the limit without creating a conversation or downloading the file.
	if got := f.next(t); !strings.Contains(got.Text, "20 MB") {
		t.Fatalf("size reply = %+v", got)
	}
	if len(d.chats(t)) != 0 {
		t.Fatal("oversize file created a conversation")
	}
	for {
		select {
		case ev := <-f.events:
			if ev.Method == "getFile" {
				t.Fatal("oversize file was downloaded")
			}
		default:
			return
		}
	}
}

func TestUserSeesInvalidTelegramTokenStatus(t *testing.T) {
	// Given a Bot API that rejects getUpdates.
	f := newFakeTelegram(t)
	f.failures["getUpdates"] = []platformFailure{{Code: 401}}
	d := startTelegram(t, f, t.TempDir(), 42)
	// When the Channels page reads runtime status.
	// Then it sees an error without the bot token.
	waitTelegramState(t, d, "error")
}

func TestUserSeesTelegramPollingErrorAndRecovery(t *testing.T) {
	// Given a Bot API whose first poll fails.
	f := newFakeTelegram(t)
	f.failures["getUpdates"] = []platformFailure{{Code: 500}}
	d := startTelegram(t, f, t.TempDir(), 42)
	// When the poll fails, the Channels page shows the failure.
	waitTelegramState(t, d, "error")
	// Then polling resumes and the Channels page shows running.
	waitTelegramState(t, d, "running")
}

func waitTelegramState(t *testing.T, d *dex, state string) {
	t.Helper()
	eventually(t, "Telegram status "+state, func() (bool, any) {
		var got struct {
			Telegram settings.Telegram `json:"telegram"`
		}
		d.must(t, http.MethodGet, "/api/settings", nil, &got)
		if strings.Contains(got.Telegram.Status.Reason, fakeToken) {
			t.Fatal("status exposed the token")
		}
		return got.Telegram.Status.State == state, got.Telegram.Status
	})
}
