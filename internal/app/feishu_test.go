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
	pingInterval         int
	conn                 *websocket.Conn
	server               *httptest.Server
	events               chan []byte
	ack                  chan struct{}
	mu                   sync.Mutex
	messages             []string
	methods              []string
	rejectCards          bool
	fail                 bool
	next                 int
	startupFailures      int
	startupRequests      int
	disconnect           bool
	connections          int
	disconnectAfterEvent bool
	cardFailures         int
	textAttempts         int
	resourceStatus       int
	resourceSize         int
}

func newFakeFeishu(t *testing.T) *fakeFeishu {
	t.Helper()
	f := &fakeFeishu{pingInterval: 120, events: make(chan []byte, 32), ack: make(chan struct{}, 32)}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}
func (f *fakeFeishu) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/callback/ws/endpoint":
		f.mu.Lock()
		f.startupRequests++
		fail := f.startupRequests <= f.startupFailures
		f.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":1}`))
			return
		}
		_ = json.MarshalWrite(w, map[string]any{"code": 0, "data": map[string]any{"URL": "ws://" + r.Host + "/ws?service_id=9", "ClientConfig": map[string]int{"ReconnectCount": 0, "ReconnectInterval": 120, "ReconnectNonce": 0, "PingInterval": f.pingInterval}}})
	case "/ws":
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		f.mu.Lock()
		f.connections++
		f.conn = conn
		disconnect := f.disconnect && f.connections == 1
		f.mu.Unlock()
		if disconnect {
			return
		}

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
				if strings.Contains(string(data), "ping") {
					_ = conn.Write(ctx, websocket.MessageBinary, []byte(strings.Replace(string(data), "ping", "pong", 1)))
					continue
				}
				// Data frames end with the acknowledgement JSON payload; pings do not.
				if strings.Contains(string(data), `{"code":200}`) {
					f.ack <- struct{}{}
					f.mu.Lock()
					disconnect := f.disconnectAfterEvent
					f.disconnectAfterEvent = false
					f.mu.Unlock()
					if disconnect {
						_ = conn.CloseNow()
						return
					}
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
			f.mu.Lock()
			status, size := f.resourceStatus, f.resourceSize
			f.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("resource not found"))
				return
			}
			if size > 0 {
				_, _ = w.Write([]byte(strings.Repeat("x", size)))
				return
			}
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
		if r.Method == http.MethodPost {
			if strings.Contains(body.Content, `"elements"`) {
				if f.cardFailures > 0 {
					f.cardFailures--
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"code":1}`))
					return
				}
			} else {
				f.textAttempts++
			}
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
	f.text(t, "owner-pair", "owner", "p2p", "/pair "+result.PairingCode)
	if chats := d.chats(t); len(chats) != 0 {
		t.Fatalf("pair retry reached agent: %+v", chats)
	}
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
	// When the final patch fails temporarily, then a later patch restores runtime status.
	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()
	once.Do(func() { close(release) })
	waitFeishuStatus(t, d, "error")
	f.mu.Lock()
	f.fail = false
	f.mu.Unlock()
	f.waitReply(t, "bash: streamed answer")
	waitFeishuStatus(t, d, "running")
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
		QRURL   string `json:"qrUrl"`
		Session string `json:"session"`
		Status  string `json:"status"`
	}
	d.must(t, http.MethodPost, "/api/settings/feishu/registration", map[string]string{"brand": "feishu"}, &setup)
	if !strings.Contains(setup.QRURL, "addons=") || !strings.Contains(setup.QRURL, "createOnly=true") {
		t.Fatal("missing setup parameters")
	}
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
	if !strings.Contains(body, `"appId":"qr-app"`) {
		t.Fatalf("missing saved app ID: %s", body)
	}
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

func TestUserGetsFeishuReplyAfterStartupFailure(t *testing.T) {
	// Given a platform whose first startup request and REST retries fail.
	f := newFakeFeishu(t)
	f.startupFailures = 3
	d := startFeishu(t, f, t.TempDir())
	// When the owner pairs later, then the bot recovers without a settings change.
	pairFeishu(t, d, f)
	f.text(t, "recovered", "owner", "p2p", "startup recovered")
	f.waitReply(t, "echo: startup recovered")
}

func TestUserGetsFeishuReplyAfterFiniteReconnectCount(t *testing.T) {
	// Given a platform that disconnects once and advertises no reconnects.
	f := newFakeFeishu(t)
	f.disconnect = true
	d := startFeishu(t, f, t.TempDir())
	// When the connection recovers, then the bot still delivers replies.
	pairFeishu(t, d, f)
	f.text(t, "reconnected", "owner", "p2p", "connection recovered")
	f.waitReply(t, "echo: connection recovered")
}

func TestUserDoesNotGetTextFallbackForTemporaryFeishuFailure(t *testing.T) {
	// Given a platform that temporarily fails all three card attempts.
	f := newFakeFeishu(t)
	d := startFeishu(t, f, t.TempDir())
	pairFeishu(t, d, f)
	f.mu.Lock()
	f.cardFailures = 3
	before := f.textAttempts
	f.mu.Unlock()
	// When durable delivery retries, then it keeps the card identity and format.
	f.text(t, "temporary-card", "owner", "p2p", "/help")
	f.waitReply(t, "/stop stops")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.textAttempts != before {
		t.Fatalf("temporary failure sent text fallback: %d", f.textAttempts-before)
	}
}

func waitFeishuStatus(t *testing.T, d *dex, want string) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		var result struct {
			Status string `json:"status"`
		}
		d.must(t, http.MethodGet, "/api/settings/feishu", nil, &result)
		if result.Status == want {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("status=%s want=%s", result.Status, want)
		}
	}
}

func TestUserSeesFeishuPreviewAfterReconnect(t *testing.T) {
	// Given a real tool that waits while its input socket disconnects.
	f := newFakeFeishu(t)
	d := startFeishu(t, f, t.TempDir())
	pairFeishu(t, d, f)
	d.setModel(t, "bash")
	release := make(chan struct{})
	var once sync.Once
	tool := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
			_, _ = w.Write([]byte("after reconnect"))
		case <-r.Context().Done():
		}
	}))
	defer tool.Close()
	defer once.Do(func() { close(release) })
	f.mu.Lock()
	f.disconnectAfterEvent = true
	f.mu.Unlock()
	// When the input connection closes before the preview tick, then the preview still appears.
	f.text(t, "preview-reconnect", "owner", "p2p", "curl -fsS "+tool.URL)
	f.waitReply(t, "Thinking")
	once.Do(func() { close(release) })
	f.waitReply(t, "bash: after reconnect")
}

func TestUserInvalidFeishuMessageDoesNotBlockLaterInput(t *testing.T) {
	// Given a paired app and invalid platform content.
	f := newFakeFeishu(t)
	d := startFeishu(t, f, t.TempDir())
	pairFeishu(t, d, f)
	// When dex rejects invalid content and a path as a file name, then it acknowledges both.
	f.message(t, "bad-json", "owner", "p2p", "text", `{`)
	f.message(t, "bad-name", "owner", "p2p", "file", `{"file_key":"file-key","file_name":"../notes.txt"}`)
	f.mu.Lock()
	f.resourceStatus = http.StatusNotFound
	f.mu.Unlock()
	f.message(t, "missing-resource", "owner", "p2p", "file", `{"file_key":"missing","file_name":"notes.txt"}`)
	f.mu.Lock()
	f.resourceStatus = 0
	f.resourceSize = (20 << 20) + 1
	f.mu.Unlock()
	f.message(t, "too-large", "owner", "p2p", "file", `{"file_key":"large","file_name":"notes.txt"}`)
	f.mu.Lock()
	f.resourceSize = 0
	f.mu.Unlock()
	if chats := d.chats(t); len(chats) != 0 {
		t.Fatalf("invalid input stored: %+v", chats)
	}
	// Then valid input still gets a reply.
	f.text(t, "valid-later", "owner", "p2p", "valid later")
	f.waitReply(t, "echo: valid later")
}

func TestUserCancelsFeishuSetupWhileAccountsServiceWaits(t *testing.T) {
	for _, action := range []string{"begin", "poll"} {
		t.Run(action, func(t *testing.T) {
			// Given a registration request that waits at the accounts boundary.
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			var once sync.Once
			accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
					return
				}
				if r.Form.Get("action") == action {
					entered <- struct{}{}
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				if r.Form.Get("action") == "begin" {
					_, _ = w.Write([]byte(`{"device_code":"device","verification_uri_complete":"https://accounts.feishu.cn/verify","interval":1,"expires_in":300}`))
				} else {
					_, _ = w.Write([]byte(`{"client_id":"late-app","client_secret":"secret","user_info":{"open_id":"owner","tenant_brand":"feishu"}}`))
				}
			}))
			defer accounts.Close()
			defer once.Do(func() { close(release) })
			f := newFakeFeishu(t)
			d := startDexWith(t, app.Config{DataDir: t.TempDir(), FeishuAccountsURL: accounts.URL, FeishuAPIURL: f.server.URL})
			path := "/api/settings/feishu/registration"
			body := map[string]string{"brand": "feishu"}
			if action == "poll" {
				var setup struct {
					Session string `json:"session"`
				}
				d.must(t, http.MethodPost, path, body, &setup)
				path += "/poll"
				body = map[string]string{"session": setup.Session}
			}
			done := make(chan int, 1)
			go func() {
				tick := time.NewTicker(20 * time.Millisecond)
				defer tick.Stop()
				for {
					status, raw := d.call(t, http.MethodPost, path, body)
					if status != http.StatusOK || action == "begin" || !strings.Contains(raw, `"pending"`) {
						done <- status
						return
					}
					select {
					case <-tick.C:
					case <-t.Context().Done():
						return
					}
				}
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("registration did not reach accounts")
			}
			// When the user turns the channel off, then cancellation completes before accounts respond.
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodDelete, d.srv.URL+"/api/settings/feishu", nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := d.srv.Client().Do(req)
			once.Do(func() { close(release) })
			if err != nil {
				t.Fatalf("settings blocked by registration: %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("delete status=%d", resp.StatusCode)
			}
			select {
			case status := <-done:
				if status != http.StatusBadRequest {
					t.Fatalf("stale registration status=%d", status)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("stale registration did not finish")
			}
			var saved struct {
				Enabled bool `json:"enabled"`
			}
			d.must(t, http.MethodGet, "/api/settings/feishu", nil, &saved)
			if saved.Enabled {
				t.Fatal("late credentials restored a canceled channel")
			}
		})
	}
}
