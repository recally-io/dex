package app_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/app"
)

// This server is the platform boundary. dex, SQLite and the worker remain real.
type fakeFeishu struct {
	reactGate            *reactionGate
	pingInterval         int
	conn                 *websocket.Conn
	server               *httptest.Server
	events               chan []byte
	ack                  chan struct{}
	failedACK            chan struct{}
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
	textFailures         int
	lostAccepts          int
	uuids                []string
	resourceStatus       int
	resourceSize         int
	expireFirstToken     bool
	tokenRequests        int
	replyAuthHeaders     []string
	deliveries           []channelSend
	destinations         map[string]string
}

func newFakeFeishu(t *testing.T) *fakeFeishu {
	t.Helper()
	f := &fakeFeishu{pingInterval: 120, events: make(chan []byte, 32), ack: make(chan struct{}, 32), failedACK: make(chan struct{}, 32), destinations: map[string]string{}}
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
				if strings.Contains(string(data), `{"code":500}`) {
					select {
					case f.failedACK <- struct{}{}:
					case <-ctx.Done():
						return
					}
					continue
				}
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
		f.mu.Lock()
		f.tokenRequests++
		token := fmt.Sprintf("tenant-token-%d", f.tokenRequests)
		f.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"code":0,"tenant_access_token":%q,"expire":7200}`, token)
	default:
		if message, path, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/"), "/reactions"); ok {
			if r.Method == http.MethodDelete {
				if f.reactGate != nil {
					f.reactGate.handle(r, message, "delete:"+strings.TrimPrefix(path, "/"))
				}
				_, _ = w.Write([]byte(`{"code":0}`))
				return
			}
			var input struct {
				Reaction struct {
					Emoji string `json:"emoji_type"`
				} `json:"reaction_type"`
			}
			if err := json.UnmarshalRead(r.Body, &input); err != nil {
				http.Error(w, "invalid reaction", http.StatusBadRequest)
				return
			}
			if f.reactGate != nil && !f.reactGate.handle(r, message, input.Reaction.Emoji) {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"code":99991400}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"code":0,"data":{"reaction_id":%q}}`, "remote-"+input.Reaction.Emoji)
			return
		}
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
			Content   string `json:"content"`
			ReceiveID string `json:"receive_id"`
			UUID      string `json:"uuid"`
		}
		if err := json.UnmarshalRead(r.Body, &body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if body.UUID != "" {
			f.uuids = append(f.uuids, body.UUID)
		}
		auth := r.Header.Get("Authorization")
		f.replyAuthHeaders = append(f.replyAuthHeaders, auth)
		if f.expireFirstToken && auth == "Bearer tenant-token-1" {
			f.expireFirstToken = false
			_, _ = w.Write([]byte(`{"code":99991663,"msg":"token expired"}`))
			return
		}
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
				if f.textFailures > 0 {
					f.textFailures--
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"code":1}`))
					return
				}
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
		if r.Method == http.MethodPost && f.lostAccepts > 0 && !strings.Contains(body.Content, "Thinking") && !strings.Contains(body.Content, "Paired.") {
			f.lostAccepts--
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":500,"msg":"lost response after remote accept"}`))
			return
		}
		peer := "feishu:" + body.ReceiveID
		if r.Method == http.MethodPatch {
			peer = f.destinations[strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/")]
		} else {
			f.destinations[fmt.Sprint("reply-", f.next)] = peer
		}
		var content struct {
			Text     string `json:"text"`
			Elements []struct {
				Content string `json:"content"`
			} `json:"elements"`
		}
		_ = json.Unmarshal([]byte(body.Content), &content)
		for _, element := range content.Elements {
			content.Text += element.Content
		}
		f.deliveries = append(f.deliveries, channelSend{Peer: peer, Text: content.Text})
		_ = json.MarshalWrite(w, map[string]any{"code": 0, "data": map[string]string{"message_id": fmt.Sprint("reply-", f.next)}})
	}
}
func (f *fakeFeishu) message(t *testing.T, id, user, sessionType, kind, content string) {
	t.Helper()
	body := map[string]any{"header": map[string]string{"event_type": "im.message.receive_v1"}, "event": map[string]any{"sender": map[string]any{"sender_type": "user", "sender_id": map[string]string{"open_id": user}}, "message": map[string]string{"message_id": id, "chat_type": sessionType, "message_type": kind, "content": content}}}
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
func (f *fakeFeishu) text(t *testing.T, id, user, sessionType, text string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"text": text})
	f.message(t, id, user, sessionType, "text", string(b))
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
	d.must(t, http.MethodPut, "/api/bots/default/channels/feishu", map[string]string{"appId": "app", "appSecret": "app-secret", "brand": "feishu"}, &result)
	if result.PairingCode == "" {
		t.Fatal("missing one-time code")
	}
	// A group with the correct code must not capture ownership.
	f.text(t, "group-pair", "stranger", "group", "/pair "+result.PairingCode)
	f.text(t, "wrong-pair", "stranger", "p2p", "/pair invalid")
	f.text(t, "owner-pair", "owner", "p2p", "/pair "+result.PairingCode)
	f.waitReply(t, "Paired.")
	f.text(t, "owner-pair", "owner", "p2p", "/pair "+result.PairingCode)
	if sessions := d.sessions(t); len(sessions) != 0 {
		t.Fatalf("pair retry reached bot: %+v", sessions)
	}
	assertNoStoredInput(t, d)
	_, body := d.call(t, http.MethodGet, "/api/bots/default/channels/feishu", nil)
	if strings.Contains(body, "app-secret") || strings.Contains(body, result.PairingCode) || !strings.Contains(body, `"paired":true`) {
		t.Fatalf("unsafe settings response: %s", body)
	}
}

func TestUserSessionsWithFeishuOwnerOnlyAndClearsMain(t *testing.T) {
	t.Parallel()
	// Given a manually configured bot, paired by the owner in a private chat.
	f := newFakeFeishu(t)
	dataDir := t.TempDir()
	d := startFeishu(t, f, dataDir)
	pairFeishu(t, d, f)
	// When other users and groups send input, then dex creates no session.
	f.text(t, "stranger-message", "stranger", "p2p", "not allowed")
	f.text(t, "group-message", "owner", "group", "not allowed")
	if sessions := d.sessions(t); len(sessions) != 0 {
		t.Fatalf("unauthorized sessions: %+v", sessions)
	}
	assertNoStoredInput(t, d)
	// When the platform repeats an owner's input, then dex stores one message.
	f.text(t, "first", "owner", "p2p", "hello owner")
	f.text(t, "first", "owner", "p2p", "hello owner")
	f.waitReply(t, "echo: hello owner")
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
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
	if sessions := d.sessions(t); len(sessions) != 0 {
		t.Fatalf("channel in Sessions: %+v", sessions)
	}
	d.watch(t, d.mainSession(t)).until(t, "owner input in main", answered("echo: hello owner"))
	// When /new is repeated, main clears once and keeps the same timeline.
	f.text(t, "new-command", "owner", "p2p", "/new")
	f.text(t, "new-command", "owner", "p2p", "/new")
	f.text(t, "second", "owner", "p2p", "second session")
	f.waitReply(t, "echo: second session")
	if sessions := d.sessions(t); len(sessions) != 0 {
		t.Fatalf("channel in Sessions: %+v", sessions)
	}
	f.text(t, "help", "owner", "p2p", "/help")
	f.waitReply(t, "/stop stops")
}

func TestUserReceivesFeishuFileAndReplyAfterRestart(t *testing.T) {
	t.Parallel()
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
	for d.mainInfo(t).Busy {
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
	if sessions := d.sessions(t); len(sessions) != 0 {
		t.Fatalf("channel in Sessions: %+v", sessions)
	}
	f.text(t, "after-restart", "owner", "p2p", "owner survived restart")
	f.waitReply(t, "echo: owner survived restart")
}

func TestUserGetsPlainTextWhenFeishuRejectsCards(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	d.must(t, http.MethodPost, "/api/bots/default/channels/feishu/registration", map[string]string{"brand": "feishu"}, &setup)
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
		d.must(t, http.MethodPost, "/api/bots/default/channels/feishu/registration/poll", map[string]string{"session": setup.Session}, &setup)
	}
	_, body := d.call(t, http.MethodGet, "/api/bots/default/channels/feishu", nil)
	if !strings.Contains(body, `"appId":"qr-app"`) {
		t.Fatalf("missing saved app ID: %s", body)
	}
	if strings.Contains(body, "private-secret") || strings.Contains(body, "private-device") {
		t.Fatal("registration disclosed credentials")
	}
	f.text(t, "qr-stranger", "stranger", "p2p", "not the creator")
	if sessions := d.sessions(t); len(sessions) != 0 {
		t.Fatal("stranger was admitted")
	}
	assertNoStoredInput(t, d)
	f.text(t, "qr-owner", "creator", "p2p", "hello from creator")
	f.waitReply(t, "echo: hello from creator")
}

func TestUserStopsFeishuAnswer(t *testing.T) {
	t.Parallel()
	// Given a real tool call that waits for a local system boundary.
	f := newFakeFeishu(t)
	d := startFeishu(t, f, t.TempDir())
	pairFeishu(t, d, f)
	d.setModel(t, "bash")
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var once sync.Once
	toolServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-release:
			_, _ = w.Write([]byte("released"))
		case <-r.Context().Done():
		}
	}))
	defer toolServer.Close()
	defer once.Do(func() { close(release) })
	f.text(t, "wait", "owner", "p2p", "curl -fsS "+toolServer.URL)
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("tool did not start")
	}
	// When the owner types /stop, then the run gets a final unanswered card.
	f.text(t, "stop", "owner", "p2p", "/stop")
	f.waitReply(t, "No answer:")
}

func TestUserGetsFeishuReplyAfterStartupFailure(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestUserInvalidFeishuMessageDoesNotBlockLaterInput(t *testing.T) {
	t.Parallel()
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
	if sessions := d.sessions(t); len(sessions) != 0 {
		t.Fatalf("invalid input stored: %+v", sessions)
	}
	assertNoStoredInput(t, d)
	// Then valid input still gets a reply.
	f.text(t, "valid-later", "owner", "p2p", "valid later")
	f.waitReply(t, "echo: valid later")
}

func TestUserCancelsFeishuSetupWhileAccountsServiceWaits(t *testing.T) {
	t.Parallel()
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
			path := "/api/bots/default/channels/feishu/registration"
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
			req, err := http.NewRequestWithContext(ctx, http.MethodDelete, d.srv.URL+"/api/bots/default/channels/feishu", nil)
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
			d.must(t, http.MethodGet, "/api/bots/default/channels/feishu", nil, &saved)
			if saved.Enabled {
				t.Fatal("late credentials restored a canceled channel")
			}
		})
	}
}

func TestUserFeishuDisconnectDoesNotStrandDurableAdmission(t *testing.T) {
	t.Parallel()
	// Given native admission paused for maintenance.
	f := newFakeFeishu(t)
	f.pingInterval = 1
	dataDir := t.TempDir()
	d := startFeishu(t, f, dataDir)
	pairFeishu(t, d, f)
	d.must(t, http.MethodPost, "/api/maintenance", nil, nil)
	conn := controlWriteDB(t, dataDir)
	webSession := d.newSession(t)
	webID := uuid.NewV7().String()
	d.must(t, http.MethodPost, fmt.Sprintf("/api/bots/default/sessions/%d/messages", webSession.ID), map[string]string{"id": webID, "text": "web input during maintenance"}, nil, http.StatusConflict)
	var webReceipts int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE id=?", webID).Scan(&webReceipts); err != nil {
		t.Fatal(err)
	}
	if webReceipts != 0 {
		t.Fatal("maintenance rejection saved a web receipt")
	}
	state := func(source string) string {
		var status string
		err := conn.QueryRowContext(t.Context(), "SELECT status FROM submissions WHERE source_key = ?", source).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return "absent"
		}
		if err != nil {
			t.Fatal(err)
		}
		return status
	}
	body, err := json.Marshal(map[string]any{
		"header": map[string]string{"event_type": "im.message.receive_v1"},
		"event": map[string]any{
			"sender":  map[string]any{"sender_type": "user", "sender_id": map[string]string{"open_id": "owner"}},
			"message": map[string]string{"message_id": "cancel-window", "chat_type": "p2p", "message_type": "text", "content": `{"text":"stored before disconnect"}`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.events <- body
	eventually(t, "Feishu receipt stored while native admission is paused", func() (bool, any) {
		s := state("feishu:app:cancel-window")
		return s == "pending", s
	})
	// When the input socket closes after the database commit but before native admission.
	f.mu.Lock()
	socket, before := f.conn, f.connections
	f.mu.Unlock()
	if err := socket.CloseNow(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Feishu reconnect after a durable receipt", func() (bool, any) { f.mu.Lock(); defer f.mu.Unlock(); return f.connections > before, f.connections })
	var admitted sql.NullInt64
	if err := conn.QueryRowContext(t.Context(), "SELECT admitted_at FROM submissions WHERE source_key=?", "feishu:app:cancel-window").Scan(&admitted); err != nil {
		t.Fatal(err)
	}
	if admitted.Valid {
		t.Fatal("maintenance receipt claims native admission")
	}
	// Repeat the platform event while the original receipt still lacks admission.
	f.text(t, "cancel-window", "owner", "p2p", "stored before disconnect")
	d.must(t, http.MethodDelete, "/api/maintenance", nil, nil)
	f.text(t, "after-window", "owner", "p2p", "after maintenance")
	f.waitReply(t, "echo: after maintenance")
	// The platform repeats the original event after reconnect.
	f.text(t, "cancel-window", "owner", "p2p", "stored before disconnect")
	f.text(t, "after-redelivery", "owner", "p2p", "after platform retry")
	f.waitReply(t, "echo: after platform retry")
	// Then an already stored input must reach native execution without restarting dex.
	got := state("feishu:app:cancel-window")
	t.Logf("original=%s later=%s after-retry=%s", got, state("feishu:app:after-window"), state("feishu:app:after-redelivery"))
	var n int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE source_key=?", "feishu:app:cancel-window").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("platform retry created %d receipts", n)
	}
	if got != "done" {
		t.Fatalf("stored input was stranded after disconnect and platform retry: status=%s", got)
	}
}

func TestUserFeishuRegistrationFollowsLarkBrandRedirect(t *testing.T) {
	t.Parallel()
	f := newFakeFeishu(t)
	var polls int
	accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("action") == "begin" {
			_, _ = w.Write([]byte(`{"device_code":"dev-lark","verification_uri_complete":"https://accounts.feishu.cn/verify","interval":1,"expires_in":300}`))
			return
		}
		polls++
		if polls == 1 {
			_, _ = w.Write([]byte(`{"user_info":{"tenant_brand":"lark"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"client_id":"lark-app","client_secret":"lark-secret","user_info":{"open_id":"lark-owner","tenant_brand":"lark"}}`))
	}))
	defer accounts.Close()
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), FeishuAPIURL: f.server.URL, FeishuAccountsURL: accounts.URL})
	d.setModel(t, "echo")
	var setup struct {
		Session string `json:"session"`
		Status  string `json:"status"`
	}
	d.must(t, http.MethodPost, "/api/bots/default/channels/feishu/registration", map[string]string{"brand": "feishu"}, &setup)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for setup.Status != "created" {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("lark registration did not finish")
		}
		d.must(t, http.MethodPost, "/api/bots/default/channels/feishu/registration/poll", map[string]string{"session": setup.Session}, &setup)
	}
	if polls != 2 {
		t.Fatalf("poll attempts = %d, want 2 (redirected for lark)", polls)
	}
	var status struct {
		Brand  string `json:"brand"`
		Paired bool   `json:"paired"`
	}
	d.must(t, http.MethodGet, "/api/bots/default/channels/feishu", nil, &status)
	if status.Brand != "lark" || !status.Paired {
		t.Fatalf("feishu status after lark redirect = %+v", status)
	}
	f.text(t, "lark-hello", "lark-owner", "p2p", "hello lark")
	f.waitReply(t, "echo: hello lark")
}

func TestUserFeishuReceivesLocalizedPostAndRefreshesExpiredToken(t *testing.T) {
	t.Parallel()
	f := newFakeFeishu(t)
	f.expireFirstToken = true
	dataDir := t.TempDir()
	d := startFeishu(t, f, dataDir)
	pairFeishu(t, d, f)

	postBody, err := json.Marshal(map[string]any{
		"zh_cn": map[string]any{
			"title": "Build",
			"content": [][]map[string]any{
				{
					{"tag": "text", "text": "@_user_1 check "},
					{"tag": "code_block", "language": "go", "text": "fmt.Println(1)"},
					{"tag": "img", "image_key": "post-img-key"},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rawEvent, err := json.Marshal(map[string]any{
		"header": map[string]string{"event_type": "im.message.receive_v1"},
		"event": map[string]any{
			"sender": map[string]any{"sender_type": "user", "sender_id": map[string]string{"open_id": "owner"}},
			"message": map[string]any{
				"message_id":   "post-1",
				"chat_type":    "p2p",
				"message_type": "post",
				"content":      string(postBody),
				"mentions":     []map[string]string{{"key": "@_user_1"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.events <- rawEvent
	select {
	case <-f.ack:
	case <-time.After(15 * time.Second):
		t.Fatal("post event was not admitted")
	}
	f.waitReply(t, "echo: Build")

	conn := sessionDB(t, d)
	var content string
	if err := conn.QueryRowContext(t.Context(), "SELECT content FROM submissions WHERE source_key = 'feishu:app:post-1'").Scan(&content); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "@_user_1") || !strings.Contains(content, "Build") || !strings.Contains(content, "```go\nfmt.Println(1)\n```") {
		t.Fatalf("parsed post content = %q", content)
	}
	files, err := filepath.Glob(filepath.Join(dataDir, "agents", "default", "workspace", "uploads", "*", "image.png"))
	if err != nil || len(files) != 1 {
		t.Fatalf("downloaded post image = %v err=%v", files, err)
	}
	f.mu.Lock()
	tokenReqs, headers := f.tokenRequests, append([]string(nil), f.replyAuthHeaders...)
	f.mu.Unlock()
	// An independent reaction client can acquire another token before the reply refresh.
	if tokenReqs < 2 || len(headers) < 2 || headers[len(headers)-1] == "Bearer tenant-token-1" {
		t.Fatalf("expired tenant token was not refreshed: tokenReqs=%d headers=%v", tokenReqs, headers)
	}
}
