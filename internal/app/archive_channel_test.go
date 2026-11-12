package app_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
)

func TestArchiveInvalidatesInflightWechatLogin(t *testing.T) {
	// Given a WeChat login whose QR status poll waits at the remote service.
	f := newFakeWechat(t)
	hold := &holdQR{entered: make(chan struct{}), release: make(chan struct{})}
	f.holdQR.Store(hold)
	released := false
	release := func() {
		if !released {
			released = true
			close(hold.release)
		}
	}
	defer release()
	d := startWechat(t, f, t.TempDir())
	bot := d.createCoder(t)
	prefix := "/api/agents/" + bot.ID
	var login wechatLogin
	d.must(t, http.MethodPost, prefix+"/channels/wechat/login", nil, &login)
	type result struct {
		status int
		body   string
	}
	done := make(chan result, 1)
	go func() {
		status, body := d.call(t, http.MethodPost, prefix+"/channels/wechat/login/"+login.ID+"/poll", map[string]string{})
		done <- result{status, body}
	}()
	waitSignal(t, hold.entered, "the QR poll reaches the remote service")
	// When the owner archives the bot and the remote service then confirms the login.
	archiveCoder(t, d)
	release()
	got := <-done
	// Then the poll is refused, no credentials are saved, and no channel starts.
	if got.status == http.StatusOK {
		t.Fatalf("the login finished after the archive: %s", got.body)
	}
	d.must(t, http.MethodDelete, prefix+"/archive", nil, nil)
	if _, body := d.call(t, http.MethodGet, prefix+"/channels", nil); strings.Contains(body, `"owner":"owner"`) {
		t.Fatalf("the login saved credentials after the archive: %s", body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.requests {
		if req.Path == "/ilink/bot/getupdates" {
			t.Fatal("an archived bot started a WeChat channel")
		}
	}
}

func TestArchiveInvalidatesInflightFeishuRegistration(t *testing.T) {
	// Given a Feishu registration whose device poll waits at the accounts service.
	f := newFakeFeishu(t)
	entered, release := make(chan struct{}), make(chan struct{})
	released := false
	unblock := func() {
		if !released {
			released = true
			close(release)
		}
	}
	defer unblock()
	accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("action") == "begin" {
			_, _ = w.Write([]byte(`{"device_code":"device","verification_uri_complete":"https://accounts.feishu.cn/verify","interval":1,"expires_in":300}`))
			return
		}
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		_, _ = w.Write([]byte(`{"client_id":"qr-app","client_secret":"private-secret","user_info":{"open_id":"creator","tenant_brand":"feishu"}}`))
	}))
	defer accounts.Close()
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), FeishuAPIURL: f.server.URL, FeishuAccountsURL: accounts.URL})
	d.setModel(t, "echo")
	bot := d.createCoder(t)
	prefix := "/api/agents/" + bot.ID + "/channels/feishu/registration"
	var setup struct {
		Session string `json:"session"`
	}
	d.must(t, http.MethodPost, prefix, map[string]string{"brand": "feishu"}, &setup)
	// The first poll waits out the 1 second interval that the registration reserves.
	time.Sleep(1100 * time.Millisecond)
	type result struct {
		status int
		body   string
	}
	done := make(chan result, 1)
	go func() {
		status, body := d.call(t, http.MethodPost, prefix+"/poll", map[string]string{"session": setup.Session})
		done <- result{status, body}
	}()
	waitSignal(t, entered, "the device poll reaches the accounts service")
	// When the owner archives the bot and the accounts service then approves the app.
	archiveCoder(t, d)
	unblock()
	got := <-done
	// Then the poll is refused, no credentials are saved, and no channel starts.
	if got.status == http.StatusOK {
		t.Fatalf("the registration finished after the archive: %s", got.body)
	}
	d.must(t, http.MethodDelete, "/api/agents/"+bot.ID+"/archive", nil, nil)
	if _, body := d.call(t, http.MethodGet, "/api/agents/"+bot.ID+"/channels", nil); strings.Contains(body, "qr-app") {
		t.Fatalf("the registration saved credentials after the archive: %s", body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startupRequests != 0 {
		t.Fatal("an archived bot started a Feishu channel")
	}
}

func TestUserArchiveClearsWechatLoginBeforeRestore(t *testing.T) {
	// Given a WeChat login whose QR status poll waits at the remote service.
	f := newFakeWechat(t)
	hold := &holdQR{entered: make(chan struct{}), release: make(chan struct{})}
	f.holdQR.Store(hold)
	released := false
	release := func() {
		if !released {
			released = true
			close(hold.release)
		}
	}
	defer release()
	d := startWechat(t, f, t.TempDir())
	bot := d.createCoder(t)
	prefix := "/api/agents/" + bot.ID
	var login wechatLogin
	d.must(t, http.MethodPost, prefix+"/channels/wechat/login", nil, &login)
	type result struct {
		status int
		body   string
	}
	done := make(chan result, 1)
	go func() {
		status, body := d.call(t, http.MethodPost, prefix+"/channels/wechat/login/"+login.ID+"/poll", map[string]string{})
		done <- result{status, body}
	}()
	waitSignal(t, hold.entered, "the QR poll reaches the remote service")
	// When the bot is archived and restored before the remote service confirms the login.
	archiveCoder(t, d)
	d.must(t, http.MethodDelete, prefix+"/archive", nil, nil)
	release()
	got := <-done
	// Then the poll is refused, no credentials are saved, and no channel starts.
	if got.status == http.StatusOK {
		t.Fatalf("the login finished after the archive: %s", got.body)
	}
	if _, body := d.call(t, http.MethodGet, prefix+"/channels", nil); strings.Contains(body, `"owner":"owner"`) {
		t.Fatalf("the login saved credentials after the archive: %s", body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.requests {
		if req.Path == "/ilink/bot/getupdates" {
			t.Fatal("an archived bot started a WeChat channel")
		}
	}
}

func TestUserWechatActivationRechecksArchivedBot(t *testing.T) {
	// Given a WeChat login whose QR status poll waits at the remote service.
	f := newFakeWechat(t)
	hold := &holdQR{entered: make(chan struct{}), release: make(chan struct{})}
	f.holdQR.Store(hold)
	released := false
	release := func() {
		if !released {
			released = true
			close(hold.release)
		}
	}
	defer release()
	d := startWechat(t, f, t.TempDir())
	bot := d.createCoder(t)
	prefix := "/api/agents/" + bot.ID
	var login wechatLogin
	d.must(t, http.MethodPost, prefix+"/channels/wechat/login", nil, &login)
	type result struct {
		status int
		body   string
	}
	done := make(chan result, 1)
	go func() {
		status, body := d.call(t, http.MethodPost, prefix+"/channels/wechat/login/"+login.ID+"/poll", map[string]string{})
		done <- result{status, body}
	}()
	waitSignal(t, hold.entered, "the QR poll reaches the remote service")
	// When archive commits before channel shutdown and the remote service confirms the login.
	conn := sessionDB(t, d)
	if _, err := conn.ExecContext(t.Context(), "UPDATE agents SET archived_at = 1 WHERE id = ?", bot.ID); err != nil {
		t.Fatal(err)
	}
	release()
	got := <-done
	// Then the poll is refused, no credentials are saved, and no channel starts.
	if got.status == http.StatusOK {
		t.Fatalf("the login finished after the archive: %s", got.body)
	}
	d.must(t, http.MethodDelete, prefix+"/archive", nil, nil)
	if _, body := d.call(t, http.MethodGet, prefix+"/channels", nil); strings.Contains(body, `"owner":"owner"`) {
		t.Fatalf("the login saved credentials after the archive: %s", body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.requests {
		if req.Path == "/ilink/bot/getupdates" {
			t.Fatal("an archived bot started a WeChat channel")
		}
	}
}

func TestUserArchiveClearsFeishuRegistrationBeforeRestore(t *testing.T) {
	// Given a Feishu registration whose device poll waits at the accounts service.
	f := newFakeFeishu(t)
	entered, release := make(chan struct{}), make(chan struct{})
	released := false
	unblock := func() {
		if !released {
			released = true
			close(release)
		}
	}
	defer unblock()
	accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("action") == "begin" {
			_, _ = w.Write([]byte(`{"device_code":"device","verification_uri_complete":"https://accounts.feishu.cn/verify","interval":1,"expires_in":300}`))
			return
		}
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		_, _ = w.Write([]byte(`{"client_id":"qr-app","client_secret":"private-secret","user_info":{"open_id":"creator","tenant_brand":"feishu"}}`))
	}))
	defer accounts.Close()
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), FeishuAPIURL: f.server.URL, FeishuAccountsURL: accounts.URL})
	d.setModel(t, "echo")
	bot := d.createCoder(t)
	prefix := "/api/agents/" + bot.ID + "/channels/feishu/registration"
	var setup struct {
		Session string `json:"session"`
	}
	d.must(t, http.MethodPost, prefix, map[string]string{"brand": "feishu"}, &setup)
	type result struct {
		status int
		body   string
	}
	done := make(chan result, 1)
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			status, body := d.call(t, http.MethodPost, prefix+"/poll", map[string]string{"session": setup.Session})
			if status != http.StatusOK || !strings.Contains(body, `"status":"pending"`) {
				done <- result{status, body}
				return
			}
			select {
			case <-t.Context().Done():
				return
			case <-ticker.C:
			}
		}
	}()
	waitSignal(t, entered, "the device poll reaches the accounts service")
	// When the owner archives the bot and the accounts service then approves the app.
	archiveCoder(t, d)
	d.must(t, http.MethodDelete, "/api/agents/"+bot.ID+"/archive", nil, nil)
	unblock()
	got := <-done
	// Then the poll is refused, no credentials are saved, and no channel starts.
	if got.status == http.StatusOK {
		t.Fatalf("the registration finished after the archive: %s", got.body)
	}
	if _, body := d.call(t, http.MethodGet, "/api/agents/"+bot.ID+"/channels", nil); strings.Contains(body, "qr-app") {
		t.Fatalf("the registration saved credentials after the archive: %s", body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startupRequests != 0 {
		t.Fatal("an archived bot started a Feishu channel")
	}
}

func TestUserFeishuActivationRechecksArchivedBot(t *testing.T) {
	// Given a Feishu registration whose device poll waits at the accounts service.
	f := newFakeFeishu(t)
	entered, release := make(chan struct{}), make(chan struct{})
	released := false
	unblock := func() {
		if !released {
			released = true
			close(release)
		}
	}
	defer unblock()
	accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("action") == "begin" {
			_, _ = w.Write([]byte(`{"device_code":"device","verification_uri_complete":"https://accounts.feishu.cn/verify","interval":1,"expires_in":300}`))
			return
		}
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		_, _ = w.Write([]byte(`{"client_id":"qr-app","client_secret":"private-secret","user_info":{"open_id":"creator","tenant_brand":"feishu"}}`))
	}))
	defer accounts.Close()
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), FeishuAPIURL: f.server.URL, FeishuAccountsURL: accounts.URL})
	d.setModel(t, "echo")
	bot := d.createCoder(t)
	prefix := "/api/agents/" + bot.ID + "/channels/feishu/registration"
	var setup struct {
		Session string `json:"session"`
	}
	d.must(t, http.MethodPost, prefix, map[string]string{"brand": "feishu"}, &setup)
	type result struct {
		status int
		body   string
	}
	done := make(chan result, 1)
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			status, body := d.call(t, http.MethodPost, prefix+"/poll", map[string]string{"session": setup.Session})
			if status != http.StatusOK || !strings.Contains(body, `"status":"pending"`) {
				done <- result{status, body}
				return
			}
			select {
			case <-t.Context().Done():
				return
			case <-ticker.C:
			}
		}
	}()
	waitSignal(t, entered, "the device poll reaches the accounts service")
	// When the owner archives the bot and the accounts service then approves the app.
	conn := sessionDB(t, d)
	if _, err := conn.ExecContext(t.Context(), "UPDATE agents SET archived_at = 1 WHERE id = ?", bot.ID); err != nil {
		t.Fatal(err)
	}
	unblock()
	got := <-done
	// Then the poll is refused, no credentials are saved, and no channel starts.
	if got.status == http.StatusOK {
		t.Fatalf("the registration finished after the archive: %s", got.body)
	}
	d.must(t, http.MethodDelete, "/api/agents/"+bot.ID+"/archive", nil, nil)
	if _, body := d.call(t, http.MethodGet, "/api/agents/"+bot.ID+"/channels", nil); strings.Contains(body, "qr-app") {
		t.Fatalf("the registration saved credentials after the archive: %s", body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startupRequests != 0 {
		t.Fatal("an archived bot started a Feishu channel")
	}
}
