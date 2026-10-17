package app_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
)

type wechatLogin struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	QRContent string `json:"qrContent"`
	Active    bool   `json:"active"`
}

// fakeWechat is the iLink and CDN boundary. It never calls a real WeChat service.
type fakeWechat struct {
	failSend      atomic.Bool
	staleSend     atomic.Bool
	waitQR        atomic.Bool
	staleRet      atomic.Int64
	staleCode     atomic.Int64
	badMedia      atomic.Bool
	mediaStatus   atomic.Int64
	badMediaQuery string
	downloads     map[string]int
	attempted     chan wechatSent
	blobs         map[string][]byte
	mediaRequests atomic.Int64
	srv           *httptest.Server
	mu            sync.Mutex
	statuses      []map[string]any
	qrCount       int
	requests      []wechatRequest
	updates       []map[string]any
	sent          chan wechatSent
}

type wechatRequest struct {
	Method string
	Path   string
	Query  string
	Body   map[string]any
	Header http.Header
}

type wechatSent struct {
	Msg struct {
		To       string `json:"to_user_id"`
		Context  string `json:"context_token"`
		ClientID string `json:"client_id"`
		Items    []struct {
			Type int `json:"type"`
			Text struct {
				Text string `json:"text"`
			} `json:"text_item"`
		} `json:"item_list"`
	} `json:"msg"`
}

func newFakeWechat(t *testing.T) *fakeWechat {
	t.Helper()
	f := &fakeWechat{sent: make(chan wechatSent, 64), attempted: make(chan wechatSent, 64), blobs: make(map[string][]byte), downloads: make(map[string]int)}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeWechat) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/c2c/download" {
		f.mediaRequests.Add(1)
		query := r.URL.Query().Get("encrypted_query_param")
		f.mu.Lock()
		f.downloads[query]++
		data := f.blobs[query]
		bad := f.badMediaQuery == "" || query == f.badMediaQuery
		f.mu.Unlock()
		if bad && f.badMedia.Load() {
			status := int(f.mediaStatus.Load())
			if status == 0 {
				status = http.StatusServiceUnavailable
			}
			w.WriteHeader(status)
			return
		}
		if data == nil {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
		return
	}

	var body map[string]any
	if r.Method == http.MethodPost {
		_ = json.UnmarshalRead(r.Body, &body)
	}
	f.mu.Lock()
	f.requests = append(f.requests, wechatRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body, Header: r.Header.Clone()})
	var out any = map[string]any{"ret": 0}
	switch r.URL.Path {
	case "/ilink/bot/get_bot_qrcode":
		f.qrCount++
		out = map[string]any{"qrcode": fmt.Sprintf("qr%d", f.qrCount), "qrcode_img_content": fmt.Sprintf("https://weixin.qq.com/qr%d", f.qrCount)}
	case "/ilink/bot/get_qrcode_status":
		if f.waitQR.Load() {
			f.mu.Unlock()
			<-r.Context().Done()
			return
		}
		if len(f.statuses) > 0 {
			out = f.statuses[0]
			f.statuses = f.statuses[1:]
		} else {
			out = map[string]any{"status": "confirmed", "bot_token": "wechat-secret", "ilink_bot_id": "bot1", "ilink_user_id": "owner", "baseurl": f.srv.URL}
		}
	case "/ilink/bot/getupdates":
		if f.staleRet.Load() != 0 || f.staleCode.Load() != 0 {
			out = map[string]any{"ret": f.staleRet.Load(), "errcode": f.staleCode.Load()}
			break
		}
		cursor, _ := body["get_updates_buf"].(string)
		found := false
		for i, batch := range f.updates {
			previous := ""
			if i > 0 {
				previous, _ = f.updates[i-1]["get_updates_buf"].(string)
			}
			if cursor == previous {
				out = batch
				found = true
				break
			}
		}
		if !found {
			f.mu.Unlock()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			_ = json.MarshalWrite(w, map[string]any{"ret": 0})
			return
		}
	case "/ilink/bot/getconfig":
		out = map[string]any{"ret": 0, "typing_ticket": "ticket"}
	case "/ilink/bot/sendmessage":
		raw, _ := json.Marshal(body)
		var sent wechatSent
		_ = json.Unmarshal(raw, &sent)
		select {
		case f.attempted <- sent:
		default:
		}
		if f.staleSend.Load() {
			out = map[string]any{"errcode": -14}
			break
		}
		if f.failSend.Load() {
			out = map[string]any{"ret": 1}
			break
		}
		f.sent <- sent
	default:
	}
	f.mu.Unlock()
	_ = json.MarshalWrite(w, out)
}

func startWechat(t *testing.T, f *fakeWechat, dir string) *dex {
	t.Helper()
	d := startDexWith(t, app.Config{DataDir: dir, WechatAPIURL: f.srv.URL, WechatCDNURL: f.srv.URL + "/c2c", WechatHTTPClient: f.srv.Client(), Version: "test"})
	d.setModel(t, "echo")
	return d
}

func TestUserStartsWechatQRLogin(t *testing.T) {
	// Given dex with a local iLink server and no saved WeChat account.
	f := newFakeWechat(t)
	d := startWechat(t, f, t.TempDir())
	// When the user requests a QR code and confirms login on the phone.
	var login wechatLogin
	d.must(t, http.MethodPost, "/api/settings/wechat/login", nil, &login)
	if login.Status != "wait" || !strings.Contains(login.QRContent, "qr1") {
		t.Fatalf("QR: %+v", login)
	}
	d.must(t, http.MethodPost, "/api/settings/wechat/login/"+login.ID+"/poll", map[string]string{}, &login)
	// Then settings show the captured owner and no token or runtime cursor.
	status, raw := d.call(t, http.MethodGet, "/api/settings", nil)
	if status != http.StatusOK || !strings.Contains(raw, `"owner":"owner"`) || strings.Contains(raw, "wechat-secret") || strings.Contains(raw, "context_token") || strings.Contains(raw, "get_updates_buf") {
		t.Fatalf("settings: %d %s", status, raw)
	}
}

func (f *fakeWechat) batch(cursor string, messages ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, map[string]any{"ret": 0, "msgs": messages, "get_updates_buf": cursor, "longpolling_timeout_ms": 1000})
}

func wxText(id uint64, from, text string) map[string]any {
	return map[string]any{"message_id": id, "from_user_id": from, "message_type": 1, "message_state": 2, "context_token": fmt.Sprintf("context%d", id), "item_list": []any{map[string]any{"type": 1, "text_item": map[string]string{"text": text}}}}
}

func (f *fakeWechat) next(t *testing.T) wechatSent {
	t.Helper()
	select {
	case sent := <-f.sent:
		return sent
	case <-time.After(30 * time.Second):
		t.Fatal("WeChat sent no answer in 30 s")
		return wechatSent{}
	}
}

func wxSentText(sent wechatSent) string {
	var texts []string
	for _, it := range sent.Msg.Items {
		texts = append(texts, it.Text.Text)
	}
	return strings.Join(texts, "")
}

func connectWechat(t *testing.T, d *dex) {
	t.Helper()
	var login wechatLogin
	d.must(t, http.MethodPost, "/api/settings/wechat/login", nil, &login)
	d.must(t, http.MethodPost, "/api/settings/wechat/login/"+login.ID+"/poll", map[string]string{}, &login)
	if login.Status != "confirmed" {
		t.Fatalf("login: %+v", login)
	}
}

func wxConn(t *testing.T, dir string) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestUserWechatQRHandlesVerificationRedirectAndExpiry(t *testing.T) {
	// Given QR states from the official login protocol, including a region redirect.
	f := newFakeWechat(t)
	target := newFakeWechat(t)
	f.statuses = []map[string]any{{"status": "wait"}, {"status": "scaned"}, {"status": "need_verifycode"}, {"status": "scaned_but_redirect", "redirect_host": strings.TrimPrefix(target.srv.URL, "https://")}, {"status": "expired"}}
	target.statuses = []map[string]any{{"status": "expired"}}
	d := startWechat(t, f, t.TempDir())
	var login wechatLogin
	d.must(t, http.MethodPost, "/api/settings/wechat/login", nil, &login)
	path := "/api/settings/wechat/login/" + login.ID + "/poll"
	// When the user supplies the phone digits and the server redirects and expires QR codes.
	for _, expected := range []string{"wait", "scaned", "need_verifycode", "scaned_but_redirect", "expired", "expired", "confirmed"} {
		body := map[string]string{}
		if expected == "scaned_but_redirect" {
			body["verifyCode"] = "123456"
		}
		d.must(t, http.MethodPost, path, body, &login)
		if login.Status != expected {
			t.Fatalf("got %+v, want %s", login, expected)
		}
	}
	// Then the redirect server received a poll, the digits went to the next poll, and confirmation captured the owner.
	target.mu.Lock()
	redirected := len(target.requests)
	target.mu.Unlock()
	f.mu.Lock()
	count := f.qrCount
	var sawCode bool
	for _, req := range f.requests {
		if strings.Contains(req.Query, "verify_code=123456") {
			sawCode = true
		}
	}
	f.mu.Unlock()
	if redirected != 1 || !sawCode || count != 3 || login.Active {
		t.Fatalf("redirects=%d code=%t QR=%d login=%+v", redirected, sawCode, count, login)
	}
}

func TestUserWechatQRStopsAfterThreeRefreshes(t *testing.T) {
	for _, state := range []string{"expired", "verify_code_blocked"} {
		t.Run(state, func(t *testing.T) {
			// Given a server that repeatedly expires or blocks codes.
			f := newFakeWechat(t)
			f.statuses = []map[string]any{{"status": state}, {"status": state}, {"status": state}, {"status": state}}
			d := startWechat(t, f, t.TempDir())
			var login wechatLogin
			d.must(t, http.MethodPost, "/api/settings/wechat/login", nil, &login)
			// When the initial code and three replacements fail, the session stops.
			for range 4 {
				d.must(t, http.MethodPost, "/api/settings/wechat/login/"+login.ID+"/poll", map[string]string{}, &login)
			}
			f.mu.Lock()
			count := f.qrCount
			f.mu.Unlock()
			if login.Active || count != 4 || login.Status != state {
				t.Fatalf("login=%+v codes=%d", login, count)
			}
		})
	}
}

func TestUserWechatAlreadyBoundDoesNotInventAnOwner(t *testing.T) {
	// Given an already-bound QR response with no credentials.
	f := newFakeWechat(t)
	f.statuses = []map[string]any{{"status": "binded_redirect"}}
	d := startWechat(t, f, t.TempDir())
	var login wechatLogin
	d.must(t, http.MethodPost, "/api/settings/wechat/login", nil, &login)
	// When the user polls, no owner or token is fabricated.
	d.must(t, http.MethodPost, "/api/settings/wechat/login/"+login.ID+"/poll", map[string]string{}, &login)
	var settings struct {
		Wechat struct {
			Enabled bool `json:"enabled"`
		} `json:"wechat"`
	}
	d.must(t, http.MethodGet, "/api/settings", nil, &settings)
	if login.Active || login.Status != "binded_redirect" || settings.Wechat.Enabled {
		t.Fatalf("login=%+v settings=%+v", login, settings)
	}
}

func TestUserWechatIgnoresNonOwnerEchoesPartialAndGroups(t *testing.T) {
	// Given messages from another sender, bot echoes, unfinished messages, and a group.
	f := newFakeWechat(t)
	echo := wxText(2, "owner", "echo")
	echo["message_type"] = 2
	partial := wxText(3, "owner", "partial")
	partial["message_state"] = 1
	group := wxText(4, "owner", "group")
	group["group_id"] = "group1"
	f.batch("c1", wxText(1, "stranger", "intruder"), echo, partial, group, wxText(5, "owner", "allowed"))
	dir := t.TempDir()
	d := startWechat(t, f, dir)
	// When the owner confirms QR login, only the finished owner DM reaches the worker.
	connectWechat(t, d)
	got := f.next(t)
	if wxSentText(got) != "echo: allowed" || got.Msg.To != "owner" || got.Msg.Context != "context5" {
		t.Fatalf("reply: %+v", got)
	}
	var n int
	if err := wxConn(t, dir).QueryRowContext(t.Context(), "SELECT count(*) FROM submissions").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("submissions: %d", n)
	}
}

func wxEncrypt(t *testing.T, plain, key []byte) []byte {
	t.Helper()
	cipher, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(bytes.Clone(plain), bytes.Repeat([]byte{byte(padding)}, padding)...)
	for i := 0; i < len(padded); i += aes.BlockSize {
		cipher.Encrypt(padded[i:i+aes.BlockSize], padded[i:i+aes.BlockSize])
	}
	return padded
}

func TestUserWechatMediaBecomesUploadsAndVoiceBecomesText(t *testing.T) {
	// Given encrypted images in both supported key formats and an encrypted file.
	f := newFakeWechat(t)
	key := []byte("0123456789abcdef")
	png := pngImage(t)
	f.blobs["image-raw"] = wxEncrypt(t, png, key)
	f.blobs["image-hex"] = wxEncrypt(t, png, key)
	f.blobs["file"] = wxEncrypt(t, []byte("real file content"), key)
	rawKey := base64.StdEncoding.EncodeToString(key)
	hexKey := base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(key)))
	msg := wxText(1, "owner", "")
	msg["item_list"] = []any{
		map[string]any{"type": 1, "ref_msg": map[string]any{"title": "earlier answer"}, "text_item": map[string]string{"text": "look"}},
		map[string]any{"type": 3, "voice_item": map[string]string{"text": "spoken words"}},
		map[string]any{"type": 2, "image_item": map[string]any{"media": map[string]string{"encrypt_query_param": "image-raw", "aes_key": rawKey}}},
		map[string]any{"type": 2, "image_item": map[string]any{"aeskey": hex.EncodeToString(key), "media": map[string]string{"full_url": f.srv.URL + "/c2c/download?encrypted_query_param=image-hex", "aes_key": hexKey}}},
		map[string]any{"type": 4, "file_item": map[string]any{"file_name": "notes.txt", "media": map[string]string{"encrypt_query_param": "file", "aes_key": hexKey}}},
		map[string]any{"type": 5},
	}
	f.batch("c1", msg)
	dir := t.TempDir()
	d := startWechat(t, f, dir)
	// When the owner sends them, real uploads and chat attachments share the decrypted data.
	connectWechat(t, d)
	sent := wxSentText(f.next(t))
	if !strings.Contains(sent, "[引用: earlier answer]\nlook\nspoken words\n[video]") {
		t.Fatalf("text: %s", sent)
	}
	var raw string
	if err := wxConn(t, dir).QueryRowContext(t.Context(), "SELECT attachments FROM submissions").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var attachments []attachment
	if err := json.Unmarshal([]byte(raw), &attachments); err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 3 {
		t.Fatalf("attachments: %s", raw)
	}
	for i, a := range attachments {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, d.srv.URL+"/api/agents/default/uploads/"+a.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := d.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		expected := png
		if i == 2 {
			expected = []byte("real file content")
		}
		if !bytes.Equal(data, expected) {
			t.Fatalf("file %d differs", i)
		}
	}
}

func TestUserWechatSplitsRepliesAndKeepsSupportedMarkdown(t *testing.T) {
	// Given a long answer with Chinese text, supported Markdown, and unsupported images and headings.
	f := newFakeWechat(t)
	text := strings.Repeat("中", 4010) + "\n##### Heading\n**bold**\n`![keep](code)`\n| a | b |\n![drop](https://example.test/image)\n*中文*\n***English***\n```md\n###### code\n![keep](fenced)\n```"
	f.batch("c1", wxText(1, "owner", text))
	d := startWechat(t, f, t.TempDir())
	connectWechat(t, d)
	// When the answer is sent, no part exceeds 2000 Unicode characters.
	var answer strings.Builder
	for range 3 {
		part := f.next(t)
		if n := len([]rune(wxSentText(part))); n > 2000 {
			t.Fatalf("part has %d characters", n)
		}
		answer.WriteString(wxSentText(part))
	}
	got := answer.String()
	if strings.Contains(got, "##### Heading") || strings.Contains(got, "![drop]") || strings.Contains(got, "*中文*") || !strings.Contains(got, "**bold**\n`![keep](code)`\n| a | b |") || !strings.Contains(got, "```md\n###### code\n![keep](fenced)\n```") {
		t.Fatalf("filtered answer: %s", got)
	}
}

func TestUserWechatCursorWaitsForDurableBatchAdmission(t *testing.T) {
	// Given a batch whose second attachment cannot yet be downloaded.
	f := newFakeWechat(t)
	key := []byte("0123456789abcdef")
	f.blobs["file"] = wxEncrypt(t, []byte("later"), key)
	f.badMedia.Store(true)
	second := wxText(2, "owner", "")
	second["item_list"] = []any{map[string]any{"type": 4, "file_item": map[string]any{"file_name": "later.txt", "media": map[string]string{"encrypt_query_param": "file", "aes_key": base64.StdEncoding.EncodeToString(key)}}}}
	f.batch("after-batch", wxText(1, "owner", "first"), second)
	dir := t.TempDir()
	d := startWechat(t, f, dir)
	connectWechat(t, d)
	if wxSentText(f.next(t)) != "echo: first" {
		t.Fatal("first answer missing")
	}
	conn := wxConn(t, dir)
	eventually(t, "failed media request", func() (bool, any) { n := f.mediaRequests.Load(); return n > 0, n })
	var cursor, context string
	if err := conn.QueryRowContext(t.Context(), "SELECT cursor,context_token FROM wechat_runtime").Scan(&cursor, &context); err != nil {
		t.Fatal(err)
	}
	if cursor != "" || context != "context2" {
		t.Fatalf("cursor=%q context=%q", cursor, context)
	}
	// When the boundary recovers, the same batch is replayed and only the missing input runs.
	f.badMedia.Store(false)
	if got := wxSentText(f.next(t)); !strings.HasPrefix(got, "echo: ") {
		t.Fatal(got)
	}
	eventually(t, "cursor after all submissions", func() (bool, any) {
		var n int
		err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions").Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		err = conn.QueryRowContext(t.Context(), "SELECT cursor FROM wechat_runtime").Scan(&cursor)
		if err != nil {
			t.Fatal(err)
		}
		return n == 2 && cursor == "after-batch", fmt.Sprintf("count=%d cursor=%s", n, cursor)
	})
}

func TestUserWechatResumesCursorContextAndUndeliveredReply(t *testing.T) {
	// Given a settled owner answer whose platform send failed.
	f := newFakeWechat(t)
	f.failSend.Store(true)
	f.batch("saved-cursor", wxText(1, "owner", "survive restart"))
	dir := t.TempDir()
	d := startWechat(t, f, dir)
	connectWechat(t, d)
	var attempt wechatSent
	select {
	case attempt = <-f.attempted:
	case <-time.After(30 * time.Second):
		t.Fatal("no failed send")
	}
	conn := wxConn(t, dir)
	eventually(t, "persisted cursor", func() (bool, any) {
		var cursor string
		err := conn.QueryRowContext(t.Context(), "SELECT cursor FROM wechat_runtime").Scan(&cursor)
		return err == nil && cursor == "saved-cursor", cursor
	})
	// When dex restarts, it restores the cursor and context before the same sender resumes.
	d.stop()
	f.failSend.Store(false)
	f.mu.Lock()
	f.requests = nil
	f.mu.Unlock()
	d = startWechat(t, f, dir)
	sent := f.next(t)
	if wxSentText(sent) != "echo: survive restart" || sent.Msg.Context != "context1" || sent.Msg.ClientID != attempt.Msg.ClientID {
		t.Fatalf("redelivery: %+v, old: %+v", sent, attempt)
	}
	eventually(t, "resumed poll cursor", func() (bool, any) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, req := range f.requests {
			if req.Path == "/ilink/bot/getupdates" {
				return req.Body["get_updates_buf"] == "saved-cursor", req.Body
			}
		}
		return false, f.requests
	})
	_ = d
}

func TestUserWechatDuplicateUpdateGetsOneAnswer(t *testing.T) {
	// Given the same remote message in two batches.
	f := newFakeWechat(t)
	f.batch("c1", wxText(1, "owner", "once"))
	f.batch("c2", wxText(1, "owner", "once"), wxText(2, "owner", "after"))
	d := startWechat(t, f, t.TempDir())
	connectWechat(t, d)
	// Then the stable source key deduplicates execution.
	first, second := wxSentText(f.next(t)), wxSentText(f.next(t))
	if first != "echo: once" || second != "echo: after" {
		t.Fatalf("answers: %q %q", first, second)
	}
}

func TestUserWechatStaleLoginNeedsNewScan(t *testing.T) {
	for _, field := range []string{"ret", "errcode"} {
		t.Run(field, func(t *testing.T) {
			// Given either official stale-session error field.
			f := newFakeWechat(t)
			if field == "ret" {
				f.staleRet.Store(-14)
			} else {
				f.staleCode.Store(-14)
			}
			dir := t.TempDir()
			d := startWechat(t, f, dir)
			connectWechat(t, d)
			// When polling receives -14, status requests a new scan and the pause is durable.
			eventually(t, "needs login", func() (bool, any) {
				var s struct {
					Wechat struct {
						Status struct {
							State string `json:"state"`
						} `json:"status"`
					} `json:"wechat"`
				}
				d.must(t, http.MethodGet, "/api/settings", nil, &s)
				return s.Wechat.Status.State == "needs_login", s
			})
			var until int64
			if err := wxConn(t, dir).QueryRowContext(t.Context(), "SELECT pause_until FROM wechat_runtime").Scan(&until); err != nil {
				t.Fatal(err)
			}
			if remaining := time.Until(time.UnixMilli(until)); remaining < 59*time.Minute || remaining > time.Hour {
				t.Fatalf("pause remaining: %s", remaining)
			}
			// Then a fresh login clears the pause and wakes reply delivery.
			f.staleRet.Store(0)
			f.staleCode.Store(0)
			f.batch("new-cursor", wxText(1, "owner", "fresh login"))
			connectWechat(t, d)
			if got := wxSentText(f.next(t)); got != "echo: fresh login" {
				t.Fatal(got)
			}
		})
	}
}

func TestUserWechatCommandsStop(t *testing.T) {
	// Given an owner chat whose command source IDs are stable.
	f := newFakeWechat(t)
	f.batch("c1", wxText(1, "owner", "first"))
	d := startWechat(t, f, t.TempDir())
	connectWechat(t, d)
	if got := wxSentText(f.next(t)); got != "echo: first" {
		t.Fatal(got)
	}
	// When the next run is pending, /stop aborts it through the shared service.
	d.setModel(t, "bash")
	marker := filepath.Join(t.TempDir(), "started")
	f.batch("c3", wxText(4, "owner", fmt.Sprintf("touch '%s'; sleep 60", marker)))
	eventually(t, "WeChat run starts", func() (bool, any) { _, err := os.Stat(marker); return err == nil, err })
	f.batch("c4", wxText(5, "owner", "/stop"))
	var stopped bool
	for range 2 {
		if wxSentText(f.next(t)) == "Stop requested. Earlier actions are not undone. Processes and remote work may continue." {
			stopped = true
		}
	}
	if !stopped {
		t.Fatal("no stop acknowledgement")
	}
}

func TestUserWechatUsesOfficialRequestMetadata(t *testing.T) {
	// Given an owner message on a local API boundary.
	f := newFakeWechat(t)
	f.batch("c1", wxText(1, "owner", "headers"))
	d := startWechat(t, f, t.TempDir())
	connectWechat(t, d)
	_ = f.next(t)
	// Then each business request uses the official auth headers and dex agent identity.
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.requests {
		if req.Header.Get("iLink-App-Id") != "bot" || req.Header.Get("iLink-App-ClientVersion") != "132106" {
			t.Fatalf("app metadata: %s", req.Path)
		}
		if req.Method != http.MethodPost {
			continue
		}
		if req.Header.Get("AuthorizationType") != "ilink_bot_token" {
			t.Fatalf("auth type: %s", req.Path)
		}
		uin, err := base64.StdEncoding.DecodeString(req.Header.Get("X-WECHAT-UIN"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = strconv.ParseUint(string(uin), 10, 32); err != nil {
			t.Fatalf("UIN: %v", err)
		}
		if req.Path == "/ilink/bot/get_bot_qrcode" {
			continue
		}
		info, ok := req.Body["base_info"].(map[string]any)
		if !ok || info["channel_version"] != "2.4.10" || info["bot_agent"] != "dex/test" || req.Header.Get("Authorization") != "Bearer wechat-secret" {
			t.Fatalf("business metadata: %s", req.Path)
		}
	}
}

func TestUserWechatQRTimeoutKeepsWaiting(t *testing.T) {
	// Given a QR long poll that reaches its client timeout.
	f := newFakeWechat(t)
	f.waitQR.Store(true)
	client := *f.srv.Client()
	client.Timeout = 100 * time.Millisecond
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), WechatAPIURL: f.srv.URL, WechatHTTPClient: &client})
	var login wechatLogin
	d.must(t, http.MethodPost, "/api/settings/wechat/login", nil, &login)
	// When the poll times out, the user can continue with the same active QR session.
	d.must(t, http.MethodPost, "/api/settings/wechat/login/"+login.ID+"/poll", map[string]string{}, &login)
	if login.Status != "wait" || !login.Active {
		t.Fatalf("timed out login: %+v", login)
	}
}

func TestUserWechatCannotActivateWithoutOwner(t *testing.T) {
	// Given an incomplete confirmed result from the platform.
	f := newFakeWechat(t)
	f.statuses = []map[string]any{{"status": "confirmed", "bot_token": "secret", "ilink_bot_id": "bot1", "baseurl": f.srv.URL}}
	d := startWechat(t, f, t.TempDir())
	var login wechatLogin
	d.must(t, http.MethodPost, "/api/settings/wechat/login", nil, &login)
	// When the owner ID is absent, no account or admission rule is activated.
	status, body := d.call(t, http.MethodPost, "/api/settings/wechat/login/"+login.ID+"/poll", map[string]string{})
	if status == http.StatusOK {
		t.Fatalf("accepted missing owner: %s", body)
	}
	status, body = d.call(t, http.MethodPost, "/api/settings/wechat/login/"+login.ID+"/poll", map[string]string{})
	if status == http.StatusOK {
		t.Fatalf("repeated poll accepted missing owner: %s", body)
	}
	var settings struct {
		Wechat struct {
			Enabled bool `json:"enabled"`
		} `json:"wechat"`
	}
	d.must(t, http.MethodGet, "/api/settings", nil, &settings)
	if settings.Wechat.Enabled {
		t.Fatal("account enabled without owner")
	}
}

func TestUserWechatTypingContinuesWhileAnswerIsPending(t *testing.T) {
	// Given a tool run that waits for the test to release it.
	f := newFakeWechat(t)
	dir := t.TempDir()
	release := filepath.Join(dir, "release")
	d := startWechat(t, f, t.TempDir())
	d.setModel(t, "bash")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	f.batch("c1", wxText(1, "owner", fmt.Sprintf("while [ ! -f '%s' ]; do sleep 0.1; done; echo ready", release)))
	connectWechat(t, d)
	// Then typing is refreshed at least twice before an answer exists.
	eventually(t, "two typing updates", func() (bool, any) {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, req := range f.requests {
			if req.Path == "/ilink/bot/sendtyping" {
				if req.Body["typing_ticket"] != "ticket" || req.Body["ilink_user_id"] != "owner" || req.Body["status"] != float64(1) {
					t.Fatalf("typing request: %v", req.Body)
				}
				n++
			}
		}
		return n >= 2, n
	})
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := wxSentText(f.next(t)); got != "bash: ready" {
		t.Fatal(got)
	}
}

func TestUserWechatRescanDeliversReplyAfterSendSessionExpires(t *testing.T) {
	// Given a settled reply rejected with -14 at the send boundary.
	f := newFakeWechat(t)
	f.staleSend.Store(true)
	f.batch("c1", wxText(1, "owner", "pending delivery"))
	dir := t.TempDir()
	d := startWechat(t, f, dir)
	connectWechat(t, d)
	conn := wxConn(t, dir)
	eventually(t, "paused pending delivery", func() (bool, any) {
		var n int
		err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions, wechat_runtime WHERE status = 'done' AND delivered_at IS NULL AND pause_until > ?", time.Now().UnixMilli()).Scan(&n)
		return err == nil && n == 1, fmt.Sprintf("pending=%d err=%v", n, err)
	})
	// When the owner scans again, the same durable sender resumes the saved answer.
	f.staleSend.Store(false)
	connectWechat(t, d)
	if got := wxSentText(f.next(t)); got != "echo: pending delivery" {
		t.Fatal(got)
	}
}

func TestUserWechatFullMessageIDRangeSurvivesBatchReplay(t *testing.T) {
	// Given the largest protocol IDs, including a message outside the owner filter.
	f := newFakeWechat(t)
	const high uint64 = 1 << 63
	const last uint64 = 1<<64 - 1
	f.batch("first", wxText(high, "stranger", "ignore"), wxText(high+1, "owner", "first"))
	f.batch("replay", wxText(high+1, "owner", "first"), wxText(last, "owner", "last"))
	f.batch("older-replay", wxText(high+1, "owner", "first"))
	dir := t.TempDir()
	d := startWechat(t, f, dir)
	// When the batches arrive, the replay does not run the first message twice.
	connectWechat(t, d)
	if got := wxSentText(f.next(t)); got != "echo: first" {
		t.Fatal(got)
	}
	if got := wxSentText(f.next(t)); got != "echo: last" {
		t.Fatal(got)
	}
	conn := wxConn(t, dir)
	eventually(t, "full uint64 watermark and cursor", func() (bool, any) {
		var cursor, watermark, token string
		err := conn.QueryRowContext(t.Context(), "SELECT cursor, context_message_id, context_token FROM wechat_runtime").Scan(&cursor, &watermark, &token)
		return err == nil && cursor == "older-replay" && watermark == "18446744073709551615" && token == "context18446744073709551615", fmt.Sprintf("%s %s %s %v", cursor, watermark, token, err)
	})
	var sources string
	if err := conn.QueryRowContext(t.Context(), "SELECT group_concat(source_key, ',') FROM (SELECT source_key FROM submissions ORDER BY source_key)").Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if sources != "wechat:bot1:18446744073709551615,wechat:bot1:9223372036854775809" {
		t.Fatal(sources)
	}
}

func TestUserWechatRetriesLocalActivationAfterFailedSave(t *testing.T) {
	// Given a database failure during runtime initialization after a valid remote confirmation.
	f := newFakeWechat(t)
	dir := t.TempDir()
	d := startWechat(t, f, dir)
	conn := wxConn(t, dir)
	if _, err := conn.ExecContext(t.Context(), "CREATE TRIGGER fail_wechat_save BEFORE INSERT ON wechat_runtime BEGIN SELECT RAISE(ABORT, 'test interrupted activation'); END"); err != nil {
		t.Fatal(err)
	}
	var login wechatLogin
	d.must(t, http.MethodPost, "/api/settings/wechat/login", nil, &login)
	path := "/api/settings/wechat/login/" + login.ID + "/poll"
	status, body := d.call(t, http.MethodPost, path, map[string]string{})
	if status == http.StatusOK {
		t.Fatalf("failed save reported success: %s", body)
	}
	var n int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM wechat_settings").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("failed activation left saved credentials")
	}
	// When storage recovers, the same candidate can activate without another remote confirmation.
	if _, err := conn.ExecContext(t.Context(), "DROP TRIGGER fail_wechat_save"); err != nil {
		t.Fatal(err)
	}
	d.must(t, http.MethodPost, path, map[string]string{}, &login)
	if login.Active || login.Status != "confirmed" {
		t.Fatalf("login: %+v", login)
	}
	f.mu.Lock()
	polls := 0
	for _, req := range f.requests {
		if req.Path == "/ilink/bot/get_qrcode_status" {
			polls++
		}
	}
	f.mu.Unlock()
	if polls != 1 {
		t.Fatalf("confirmation was consumed, remote polls=%d", polls)
	}
	f.batch("after-retry", wxText(1, "owner", "activated"))
	if got := wxSentText(f.next(t)); got != "echo: activated" {
		t.Fatal(got)
	}
}

func wxFile(query, name string) map[string]any {
	return map[string]any{"type": 4, "file_item": map[string]any{"file_name": name, "media": map[string]string{"encrypt_query_param": query, "aes_key": base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))}}}
}

func TestUserWechatTemporaryCDNFailuresKeepBatch(t *testing.T) {
	for _, code := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			// Given a temporary CDN rejection of an owner attachment.
			f := newFakeWechat(t)
			f.mediaStatus.Store(int64(code))
			f.badMedia.Store(true)
			f.blobs["file"] = wxEncrypt(t, []byte("recover"), []byte("0123456789abcdef"))
			msg := wxText(1, "owner", "")
			msg["item_list"] = []any{wxFile("file", "retry.txt")}
			f.batch("recovered", msg)
			dir := t.TempDir()
			d := startWechat(t, f, dir)
			connectWechat(t, d)
			conn := wxConn(t, dir)
			eventually(t, "temporary receive error", func() (bool, any) {
				var s struct {
					Wechat struct {
						Status struct {
							State string `json:"state"`
						} `json:"status"`
					} `json:"wechat"`
				}
				d.must(t, http.MethodGet, "/api/settings", nil, &s)
				return s.Wechat.Status.State == "error", s
			})
			var cursor string
			if err := conn.QueryRowContext(t.Context(), "SELECT cursor FROM wechat_runtime").Scan(&cursor); err != nil {
				t.Fatal(err)
			}
			if cursor != "" {
				t.Fatalf("temporary HTTP %d lost the message: cursor=%s", code, cursor)
			}
			// When the CDN recovers, the existing receive retry admits the message once.
			f.badMedia.Store(false)
			_ = f.next(t)
			eventually(t, "cursor commits after CDN recovery", func() (bool, any) {
				var n int
				err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions, wechat_runtime WHERE cursor = 'recovered'").Scan(&n)
				return err == nil && n == 1, fmt.Sprintf("n=%d err=%v", n, err)
			})
		})
	}
}

func TestUserWechatReplaySkipsAdmittedMediaAndCleansPartialUploads(t *testing.T) {
	// Given an admitted file followed by a message with two files, whose second download fails.
	f := newFakeWechat(t)
	key := []byte("0123456789abcdef")
	for _, query := range []string{"admitted", "partial", "blocked"} {
		f.blobs[query] = wxEncrypt(t, []byte(query), key)
	}
	f.badMediaQuery = "blocked"
	f.badMedia.Store(true)
	first, second := wxText(1, "owner", ""), wxText(2, "owner", "")
	first["item_list"] = []any{wxFile("admitted", "admitted.txt")}
	second["item_list"] = []any{wxFile("partial", "partial.txt"), wxFile("blocked", "blocked.txt")}
	f.batch("complete", first, second)
	dir := t.TempDir()
	d := startWechat(t, f, dir)
	connectWechat(t, d)
	_ = f.next(t)
	// Then only the admitted upload remains after the failed conversion.
	eventually(t, "partial conversion ends", func() (bool, any) {
		var s struct {
			Wechat struct {
				Status struct {
					State string `json:"state"`
				} `json:"status"`
			} `json:"wechat"`
		}
		d.must(t, http.MethodGet, "/api/settings", nil, &s)
		return s.Wechat.Status.State == "error", s
	})
	uploads := filepath.Join(dir, "agents", app.DefaultAgentID, "workspace", "uploads", "*", "*")
	files, err := filepath.Glob(uploads)
	if err != nil || len(files) != 1 {
		t.Fatalf("partial attempt leaked uploads: %v %v", files, err)
	}
	// When the batch replays, the admitted attachment is not downloaded again.
	f.badMedia.Store(false)
	_ = f.next(t)
	conn := wxConn(t, dir)
	eventually(t, "batch completion", func() (bool, any) {
		var cursor string
		err := conn.QueryRowContext(t.Context(), "SELECT cursor FROM wechat_runtime").Scan(&cursor)
		return err == nil && cursor == "complete", cursor
	})
	files, err = filepath.Glob(uploads)
	if err != nil || len(files) != 3 {
		t.Fatalf("upload ownership differs: %v %v", files, err)
	}
	f.mu.Lock()
	downloads := f.downloads["admitted"]
	f.mu.Unlock()
	if downloads != 1 {
		t.Fatalf("admitted media downloaded %d times", downloads)
	}
}

func TestUserWechatRetriesRuntimeReadAfterRestart(t *testing.T) {
	// Given saved credentials and a temporary runtime read failure on restart.
	f := newFakeWechat(t)
	dir := t.TempDir()
	d := startWechat(t, f, dir)
	connectWechat(t, d)
	d.stop()
	conn := wxConn(t, dir)
	if _, err := conn.ExecContext(t.Context(), "ALTER TABLE wechat_runtime RENAME TO unavailable_runtime"); err != nil {
		t.Fatal(err)
	}
	d = startWechat(t, f, dir)
	eventually(t, "runtime read failure is visible", func() (bool, any) {
		var s struct {
			Wechat struct {
				Status struct {
					State string `json:"state"`
				} `json:"status"`
			} `json:"wechat"`
		}
		d.must(t, http.MethodGet, "/api/settings", nil, &s)
		return s.Wechat.Status.State == "error", s
	})
	// When storage recovers, Run retries without another settings change.
	if _, err := conn.ExecContext(t.Context(), "ALTER TABLE unavailable_runtime RENAME TO wechat_runtime"); err != nil {
		t.Fatal(err)
	}
	f.batch("after-storage-recovery", wxText(1, "owner", "recovered"))
	if got := wxSentText(f.next(t)); got != "echo: recovered" {
		t.Fatal(got)
	}
}

// wechatJoinTransport holds a real -14 response until the test releases the old request.
type wechatJoinTransport struct {
	base    http.RoundTripper
	once    sync.Once
	entered chan struct{}
	stopped chan struct{}
	release chan struct{}
}

func (tr *wechatJoinTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := tr.base.RoundTrip(req)
	if err == nil && req.URL.Path == "/ilink/bot/getupdates" {
		tr.once.Do(func() {
			raw, readErr := io.ReadAll(resp.Body)
			closeErr := resp.Body.Close()
			if readErr != nil || closeErr != nil {
				err = errors.Join(readErr, closeErr)
				return
			}
			resp.Body = &wechatJoinBody{ReadCloser: io.NopCloser(bytes.NewReader(raw)), ctx: req.Context(), transport: tr}
		})
	}
	return resp, err
}

type wechatJoinBody struct {
	io.ReadCloser
	ctx       context.Context
	transport *wechatJoinTransport
	once      sync.Once
}

func (b *wechatJoinBody) Read(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.transport.entered)
		<-b.ctx.Done()
		close(b.transport.stopped)
		<-b.transport.release
	})
	return b.ReadCloser.Read(p)
}

func TestUserWechatRescanJoinsOldRequestBeforeClearingPause(t *testing.T) {
	// Given an old -14 response blocked at the HTTP boundary.
	f := newFakeWechat(t)
	f.staleRet.Store(-14)
	tr := &wechatJoinTransport{base: f.srv.Client().Transport, entered: make(chan struct{}), stopped: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(tr.release) }) }
	defer unblock()
	client := *f.srv.Client()
	client.Transport = tr
	dir := t.TempDir()
	d := startDexWith(t, app.Config{DataDir: dir, WechatAPIURL: f.srv.URL, WechatHTTPClient: &client})
	d.setModel(t, "echo")
	connectWechat(t, d)
	select {
	case <-tr.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("old request did not reach barrier")
	}
	conn := wxConn(t, dir)
	until := time.Now().Add(time.Hour).UnixMilli()
	if _, err := conn.ExecContext(t.Context(), "UPDATE wechat_runtime SET pause_until = ?", until); err != nil {
		t.Fatal(err)
	}
	var login wechatLogin
	d.must(t, http.MethodPost, "/api/settings/wechat/login", nil, &login)
	// When a replacement confirms, activation cancels and joins the old request first.
	done := make(chan error, 1)
	go func() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+"/api/settings/wechat/login/"+login.ID+"/poll", strings.NewReader("{}"))
		if err != nil {
			done <- err
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := d.srv.Client().Do(req)
		if err == nil {
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("activation HTTP %d", resp.StatusCode)
			}
		}
		done <- err
	}()
	select {
	case <-tr.stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("old request was not canceled")
	}
	var stored int64
	if err := conn.QueryRowContext(t.Context(), "SELECT pause_until FROM wechat_runtime").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != until {
		unblock()
		t.Fatalf("pause reset before old request joined: got %d want %d", stored, until)
	}
	f.staleRet.Store(0)
	f.batch("fresh", wxText(1, "owner", "new credentials"))
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("activation did not finish")
	}
	if got := wxSentText(f.next(t)); got != "echo: new credentials" {
		t.Fatal(got)
	}
	if err := conn.QueryRowContext(t.Context(), "SELECT pause_until FROM wechat_runtime").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatalf("late old response paused the new bot: %d", stored)
	}
}

func TestUserWechatInterruptedCommandSkipsMediaBeforeCompletion(t *testing.T) {
	// Given an admitted main input and an interrupted command receipt.
	f := newFakeWechat(t)
	f.batch("first", wxText(1, "owner", "hello"))
	dir := t.TempDir()
	d := startWechat(t, f, dir)
	connectWechat(t, d)
	_ = f.next(t)
	conn := wxConn(t, dir)
	if _, err := conn.ExecContext(t.Context(), "INSERT INTO channel_commands (source_key, peer, channel_key, command) VALUES ('wechat:bot1:2', 'wechat:owner', 'main', '/stop')"); err != nil {
		t.Fatal(err)
	}
	// When the source replays with unavailable media, the receipt still completes.
	f.badMedia.Store(true)
	msg := wxText(2, "owner", "")
	msg["item_list"] = []any{wxFile("unavailable", "unused.txt")}
	f.batch("command-complete", msg)
	if got := wxSentText(f.next(t)); got != "Stop requested. Earlier actions are not undone. Processes and remote work may continue." {
		t.Fatal(got)
	}
	eventually(t, "command replay cursor", func() (bool, any) {
		var cursor string
		err := conn.QueryRowContext(t.Context(), "SELECT cursor FROM wechat_runtime").Scan(&cursor)
		return err == nil && cursor == "command-complete", cursor
	})
	if n := f.mediaRequests.Load(); n != 0 {
		t.Fatalf("receipt replay downloaded media %d times", n)
	}
	var completed int
	if err := conn.QueryRowContext(t.Context(), "SELECT completed FROM channel_commands WHERE source_key = 'wechat:bot1:2'").Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 1 {
		t.Fatal("interrupted command was not completed")
	}
}

func TestUserWechatRejectedMediaCleansUploadsBeforeCursorAdvance(t *testing.T) {
	// Given a message with one valid file followed by invalid media.
	f := newFakeWechat(t)
	f.blobs["valid"] = wxEncrypt(t, []byte("unused"), []byte("0123456789abcdef"))
	msg := wxText(1, "owner", "")
	msg["item_list"] = []any{wxFile("valid", "unused.txt"), map[string]any{"type": 4, "file_item": map[string]string{"file_name": "invalid.txt"}}}
	f.batch("rejected", msg)
	dir := t.TempDir()
	d := startWechat(t, f, dir)
	connectWechat(t, d)
	conn := wxConn(t, dir)
	// Then explicit rejection advances the cursor and leaves no unowned upload.
	eventually(t, "invalid media rejection", func() (bool, any) {
		var cursor string
		err := conn.QueryRowContext(t.Context(), "SELECT cursor FROM wechat_runtime").Scan(&cursor)
		return err == nil && cursor == "rejected", cursor
	})
	files, err := filepath.Glob(filepath.Join(dir, "agents", app.DefaultAgentID, "workspace", "uploads", "*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("rejected media left uploads: %v %v", files, err)
	}
	var n int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("invalid message was admitted")
	}
}
