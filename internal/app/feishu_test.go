package app_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/app"
)

// This server is the platform boundary. dex, SQLite and the worker remain real.
type fakeFeishu struct {
	server      *httptest.Server
	events      chan []byte
	ack         chan struct{}
	mu          sync.Mutex
	messages    []string
	methods     []string
	rejectCards bool
	fail        bool
	next        int
}

func newFakeFeishu(t *testing.T) *fakeFeishu {
	t.Helper()
	f := &fakeFeishu{events: make(chan []byte, 32), ack: make(chan struct{}, 32)}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}
func (f *fakeFeishu) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/callback/ws/endpoint":
		_ = json.MarshalWrite(w, map[string]any{"code": 0, "data": map[string]any{"URL": "ws://" + r.Host + "/ws?service_id=9", "ClientConfig": map[string]int{"ReconnectCount": -1, "ReconnectInterval": 1, "ReconnectNonce": 0, "PingInterval": 10}}})
	case "/ws":
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer cancel()
			for {
				_, data, err := conn.Read(ctx)
				if err != nil {
					return
				}
				// Data frames end with the acknowledgement JSON payload; pings do not.
				if strings.Contains(string(data), `{"code":200}`) {
					f.ack <- struct{}{}
				}
			}
		}()
		defer func() { cancel(); _ = conn.CloseNow(); <-done }()
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-f.events:
				b := binary.AppendUvarint(nil, 8)
				b = binary.AppendUvarint(b, 1)
				b = binary.AppendUvarint(b, 16)
				b = binary.AppendUvarint(b, 1)
				b = binary.AppendUvarint(b, 24)
				b = binary.AppendUvarint(b, 9)
				b = binary.AppendUvarint(b, 32)
				b = binary.AppendUvarint(b, 1)
				header := []byte{10, 4, 't', 'y', 'p', 'e', 18, 5, 'e', 'v', 'e', 'n', 't'}
				b = binary.AppendUvarint(b, 42)
				b = binary.AppendUvarint(b, uint64(len(header)))
				b = append(b, header...)
				b = binary.AppendUvarint(b, 66)
				b = binary.AppendUvarint(b, uint64(len(event)))
				b = append(b, event...)
				if conn.Write(ctx, websocket.MessageBinary, b) != nil {
					return
				}
			}
		}
	case "/open-apis/auth/v3/tenant_access_token/internal":
		_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"tenant-token","expire":7200}`))
	default:
		if strings.Contains(r.URL.Path, "/resources/") {
			_, _ = w.Write([]byte("file from owner"))
			return
		}
		var body struct {
			Content string `json:"content"`
		}
		if err := json.UnmarshalRead(r.Body, &body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":1}`))
			return
		}
		if f.rejectCards && strings.Contains(body.Content, `"elements"`) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":230001}`))
			return
		}
		f.methods = append(f.methods, r.Method)
		f.messages = append(f.messages, body.Content)
		f.next++
		_ = json.MarshalWrite(w, map[string]any{"code": 0, "data": map[string]string{"message_id": fmt.Sprint("reply-", f.next)}})
	}
}
func (f *fakeFeishu) message(t *testing.T, id, user, chatType, kind, content string) {
	t.Helper()
	body := map[string]any{"header": map[string]string{"event_type": "im.message.receive_v1"}, "event": map[string]any{"sender": map[string]any{"sender_type": "user", "sender_id": map[string]string{"open_id": user}}, "message": map[string]string{"message_id": id, "chat_type": chatType, "message_type": kind, "content": content}}}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	f.events <- b
	select {
	case <-f.ack:
	case <-time.After(15 * time.Second):
		t.Fatal("platform event was not admitted")
	}
}
func (f *fakeFeishu) text(t *testing.T, id, user, chatType, text string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"text": text})
	f.message(t, id, user, chatType, "text", string(b))
}
func (f *fakeFeishu) waitReply(t *testing.T, text string) {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		f.mu.Lock()
		messages := strings.Join(f.messages, "\n")
		f.mu.Unlock()
		if strings.Contains(messages, text) {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("no reply containing %q; got %s", text, messages)
		}
	}
}

func startFeishu(t *testing.T, f *fakeFeishu, dataDir string) *dex {
	t.Helper()
	d := startDexWith(t, app.Config{DataDir: dataDir, FeishuAPIURL: f.server.URL})
	d.setModel(t, "echo")
	return d
}
func pairFeishu(t *testing.T, d *dex, f *fakeFeishu) {
	t.Helper()
	var result struct {
		PairingCode string `json:"pairingCode"`
	}
	d.must(t, http.MethodPut, "/api/settings/feishu", map[string]string{"appId": "app", "appSecret": "app-secret", "brand": "feishu"}, &result)
	if result.PairingCode == "" {
		t.Fatal("missing one-time code")
	}
	// A group with the correct code must not capture ownership.
	f.text(t, "group-pair", "stranger", "group", "/pair "+result.PairingCode)
	f.text(t, "wrong-pair", "stranger", "p2p", "/pair invalid")
	f.text(t, "owner-pair", "owner", "p2p", "/pair "+result.PairingCode)
	f.waitReply(t, "Paired.")
	_, body := d.call(t, http.MethodGet, "/api/settings/feishu", nil)
	if strings.Contains(body, "app-secret") || strings.Contains(body, result.PairingCode) || !strings.Contains(body, `"paired":true`) {
		t.Fatalf("unsafe settings response: %s", body)
	}
}

func TestUserChatsWithFeishuOwnerOnlyAndStartsNewChat(t *testing.T) {
	// Given a manually configured bot, paired by the owner in a private chat.
	f := newFakeFeishu(t)
	dataDir := t.TempDir()
	d := startFeishu(t, f, dataDir)
	pairFeishu(t, d, f)
	// When other users and groups send input, then dex creates no chat.
	f.text(t, "stranger-message", "stranger", "p2p", "not allowed")
	f.text(t, "group-message", "owner", "group", "not allowed")
	if chats := d.chats(t); len(chats) != 0 {
		t.Fatalf("unauthorized chats: %+v", chats)
	}
	// When the platform repeats an owner's input, then dex stores one message.
	f.text(t, "first", "owner", "p2p", "hello owner")
	f.text(t, "first", "owner", "p2p", "hello owner")
	f.waitReply(t, "echo: hello owner")
	conn, err := sql.Open("sqlite", filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	var count int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE source_key = 'feishu:app:first'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("duplicate input stored %d submissions", count)
	}
	chats := d.chats(t)
	if len(chats) != 1 {
		t.Fatalf("chats=%+v", chats)
	}
	// When /new is repeated, then the next message starts one new chat.
	f.text(t, "new-command", "owner", "p2p", "/new")
	f.text(t, "new-command", "owner", "p2p", "/new")
	f.text(t, "second", "owner", "p2p", "second chat")
	f.waitReply(t, "echo: second chat")
	if chats = d.chats(t); len(chats) != 2 {
		t.Fatalf("chats=%+v", chats)
	}
	f.text(t, "help", "owner", "p2p", "/help")
	f.waitReply(t, "/stop stops")
}

func TestUserReceivesFeishuFileAndReplyAfterRestart(t *testing.T) {
	// Given a paired bot and a platform that cannot accept replies.
	f := newFakeFeishu(t)
	dataDir := t.TempDir()
	d := startFeishu(t, f, dataDir)
	pairFeishu(t, d, f)
	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()
	// When an attached file settles during that outage, then it remains available after restart.
	f.message(t, "file", "owner", "p2p", "file", `{"file_key":"file-key","file_name":"notes.txt"}`)
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		chats := d.chats(t)
		if len(chats) == 1 && !chats[0].Busy {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("file submission did not settle")
		}
	}
	files, err := filepath.Glob(filepath.Join(dataDir, "agents", "default", "workspace", "uploads", "*", "notes.txt"))
	if err != nil || len(files) != 1 {
		t.Fatalf("attachment paths=%v err=%v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil || string(data) != "file from owner" {
		t.Fatalf("attachment=%q err=%v", data, err)
	}
	t.Logf("resource bytes=%d, durable submission settled before restart", len(data))
	d.stop()
	f.mu.Lock()
	f.fail = false
	f.mu.Unlock()
	d = startFeishu(t, f, dataDir)
	f.waitReply(t, "echo:")
	// The owner remains authorized; the same source cannot create another submission.
	f.message(t, "file", "owner", "p2p", "file", `{"file_key":"file-key","file_name":"notes.txt"}`)
	if chats := d.chats(t); len(chats) != 1 {
		t.Fatalf("chats=%+v", chats)
	}
	f.text(t, "after-restart", "owner", "p2p", "owner survived restart")
	f.waitReply(t, "echo: owner survived restart")
}

func TestUserSeesFeishuThinkingCardBecomeFinalAnswer(t *testing.T) {
	// Given a real worker whose tool waits for a local HTTP boundary.
	f := newFakeFeishu(t)
	d := startFeishu(t, f, t.TempDir())
	pairFeishu(t, d, f)
	d.setModel(t, "bash")
	release := make(chan struct{})
	toolServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
			_, _ = w.Write([]byte("streamed answer"))
		case <-r.Context().Done():
		}
	}))
	defer toolServer.Close()
	var once sync.Once
	defer once.Do(func() { close(release) })
	// When the run waits, then dex sends a thinking card.
	f.text(t, "stream", "owner", "p2p", "curl -fsS "+toolServer.URL)
	f.waitReply(t, "Thinking")
	// When the tool finishes, then dex patches the card with the final answer.
	once.Do(func() { close(release) })
	f.waitReply(t, "bash: streamed answer")
	f.mu.Lock()
	defer f.mu.Unlock()
	found := false
	for _, method := range f.methods {
		if method == http.MethodPatch {
			found = true
		}
	}
	if !found {
		t.Fatalf("final answer did not patch the preview: %v", f.methods)
	}
}

func TestUserGetsPlainTextWhenFeishuRejectsCards(t *testing.T) {
	// Given a paired app whose platform rejects interactive cards.
	f := newFakeFeishu(t)
	d := startFeishu(t, f, t.TempDir())
	pairFeishu(t, d, f)
	f.mu.Lock()
	f.rejectCards = true
	f.mu.Unlock()
	// When the owner sends text, then dex confirms a plain text reply.
	f.text(t, "fallback", "owner", "p2p", "plain answer")
	f.waitReply(t, `"text":"echo: plain answer"`)
}

func TestUserCreatesFeishuAppAndPairsTheCreator(t *testing.T) {
	// Given a local registration endpoint and the real settings service.
	f := newFakeFeishu(t)
	accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("action") == "begin" {
			_, _ = w.Write([]byte(`{"device_code":"private-device","verification_uri_complete":"https://accounts.feishu.cn/verify","interval":1,"expires_in":300}`))
			return
		}
		_, _ = w.Write([]byte(`{"client_id":"qr-app","client_secret":"private-secret","user_info":{"open_id":"creator","tenant_brand":"feishu"}}`))
	}))
	defer accounts.Close()
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), FeishuAPIURL: f.server.URL, FeishuAccountsURL: accounts.URL})
	d.setModel(t, "echo")
	var setup struct {
		Session string `json:"session"`
		Status  string `json:"status"`
	}
	d.must(t, http.MethodPost, "/api/settings/feishu/registration", map[string]string{"brand": "feishu"}, &setup)
	if setup.Session == "" {
		t.Fatal("missing registration session")
	}
	// When the creator approves the device session, then credentials stay server-side.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for setup.Status != "created" {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("registration did not finish")
		}
		d.must(t, http.MethodPost, "/api/settings/feishu/registration/poll", map[string]string{"session": setup.Session}, &setup)
	}
	_, body := d.call(t, http.MethodGet, "/api/settings/feishu", nil)
	if strings.Contains(body, "private-secret") || strings.Contains(body, "private-device") {
		t.Fatal("registration disclosed credentials")
	}
	f.text(t, "qr-stranger", "stranger", "p2p", "not the creator")
	if chats := d.chats(t); len(chats) != 0 {
		t.Fatal("stranger was admitted")
	}
	f.text(t, "qr-owner", "creator", "p2p", "hello from creator")
	f.waitReply(t, "echo: hello from creator")
}

func TestUserStopsFeishuAnswer(t *testing.T) {
	// Given a real tool call that waits for a local system boundary.
	f := newFakeFeishu(t)
	d := startFeishu(t, f, t.TempDir())
	pairFeishu(t, d, f)
	d.setModel(t, "bash")
	release := make(chan struct{})
	var once sync.Once
	toolServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
			_, _ = w.Write([]byte("released"))
		case <-r.Context().Done():
		}
	}))
	defer toolServer.Close()
	defer once.Do(func() { close(release) })
	f.text(t, "wait", "owner", "p2p", "curl -fsS "+toolServer.URL)
	f.waitReply(t, "Thinking")
	// When the owner types /stop, then the run gets a final unanswered card.
	f.text(t, "stop", "owner", "p2p", "/stop")
	f.waitReply(t, "No answer:")
}
