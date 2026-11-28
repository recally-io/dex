package app_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
)

func TestArchiveInvalidatesInflightWechatLogin(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"archive", "restore", "db_archive"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
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
			prefix := "/api/bots/" + bot.ID
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
			// When the bot is archived (and optionally restored or updated directly in DB) while login confirms.
			switch mode {
			case "archive":
				archiveCoder(t, d)
			case "restore":
				archiveCoder(t, d)
				d.must(t, http.MethodDelete, prefix+"/archive", nil, nil)
			case "db_archive":
				conn := sessionDB(t, d)
				if _, err := conn.ExecContext(t.Context(), "UPDATE bots SET archived_at = 1 WHERE id = ?", bot.ID); err != nil {
					t.Fatal(err)
				}
			}
			release()
			got := <-done
			// Then the poll is refused, no credentials are saved, and no channel starts.
			if got.status == http.StatusOK {
				t.Fatalf("the login finished after the archive: %s", got.body)
			}
			if mode != "restore" {
				d.must(t, http.MethodDelete, prefix+"/archive", nil, nil)
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
		})
	}
}

func TestUserKeepsRestoredFeishuChannelWhenArchiveCleanupFinishesLate(t *testing.T) {
	t.Parallel()
	// Given a Feishu webhook and an open registration on a bot with Runner cleanup pending.
	cleanupStarted, releaseCleanup := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(releaseCleanup) })
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/sandboxes/old-sandbox" {
			http.NotFound(w, r)
			return
		}
		close(cleanupStarted)
		select {
		case <-releaseCleanup:
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(cloud.Close)
	defer unblock()
	accounts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("action") == "begin" {
			_, _ = w.Write([]byte(`{"device_code":"device","verification_uri_complete":"https://accounts.feishu.cn/verify","interval":1,"expires_in":300}`))
			return
		}
		_, _ = w.Write([]byte(`{"client_id":"qr-app","client_secret":"test-secret","user_info":{"open_id":"creator","tenant_brand":"feishu"}}`))
	}))
	t.Cleanup(accounts.Close)
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), FeishuAccountsURL: accounts.URL, E2BAPIURL: cloud.URL})
	var runner apiRunner
	d.must(t, http.MethodPost, "/api/settings/runners", map[string]any{
		"name": "Cloud Sandbox", "kind": "e2b", "config": map[string]any{"template": "base"}, "secret": "test-key",
	}, &runner, http.StatusCreated)
	bot := d.createCoder(t)
	bot.RunnerID = runner.ID
	base := "/api/bots/" + bot.ID
	d.must(t, http.MethodPut, base, bot, nil)
	if _, err := sessionDB(t, d).ExecContext(t.Context(), `UPDATE bots SET runner_sandbox_id = 'old-sandbox' WHERE id = ?`, bot.ID); err != nil {
		t.Fatal(err)
	}
	feishu := base + "/channels/feishu"
	d.must(t, http.MethodPut, feishu, map[string]any{
		"appId": "saved-app", "appSecret": "test-secret", "brand": "feishu", "transport": "webhook",
		"webhook": map[string]string{"verificationToken": "test-token"},
	}, nil)
	var setup struct {
		Session string `json:"session"`
	}
	d.must(t, http.MethodPost, feishu+"/registration", map[string]string{"brand": "feishu"}, &setup)
	if setup.Session == "" {
		t.Fatal("registration did not return a session")
	}
	questionSession, question := askArchive(t, d, "Coder")
	d.setModel(t, "echo")

	// When restore runs after the archive commit and before the channel stop callback.
	approved := make(chan struct{})
	go func() {
		defer close(approved)
		d.answer(t, questionSession, question.ID, "Archive Coder", http.StatusAccepted)
	}()
	waitSignal(t, cleanupStarted, "the archive callback reaches Runner cleanup")
	d.must(t, http.MethodDelete, base+"/archive", nil, nil)
	staleStatus, staleBody := d.call(t, http.MethodPost, feishu+"/registration/poll", map[string]string{"session": setup.Session})
	unblock()
	waitSignal(t, approved, "the archive callback finishes")
	d.watch(t, questionSession).until(t, "the approved archive answer arrives", answeredWith(`Approved. Bot "Coder" is archived.`))

	// Then the old registration is invalid before cleanup ends, and the restored webhook still answers.
	if staleStatus != http.StatusBadRequest {
		t.Errorf("pre-archive registration survived restore: %d %s", staleStatus, staleBody)
	}
	hook := fmt.Sprintf("/hooks/channels/feishu/%s", bot.ID)
	postChannelHook(t, d, hook, []byte(`{"type":"url_verification","token":"test-token","challenge":"restored"}`), nil, http.StatusOK)
}

func TestArchiveInvalidatesInflightFeishuRegistration(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"archive", "restore", "db_archive"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
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
			prefix := "/api/bots/" + bot.ID + "/channels/feishu/registration"
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
			// When the bot is archived (and optionally restored or updated directly in DB) while accounts approve.
			switch mode {
			case "archive":
				archiveCoder(t, d)
			case "restore":
				archiveCoder(t, d)
				d.must(t, http.MethodDelete, "/api/bots/"+bot.ID+"/archive", nil, nil)
			case "db_archive":
				conn := sessionDB(t, d)
				if _, err := conn.ExecContext(t.Context(), "UPDATE bots SET archived_at = 1 WHERE id = ?", bot.ID); err != nil {
					t.Fatal(err)
				}
			}
			unblock()
			got := <-done
			// Then the poll is refused, no credentials are saved, and no channel starts.
			if got.status == http.StatusOK {
				t.Fatalf("the registration finished after the archive: %s", got.body)
			}
			if mode != "restore" {
				d.must(t, http.MethodDelete, "/api/bots/"+bot.ID+"/archive", nil, nil)
			}
			if _, body := d.call(t, http.MethodGet, "/api/bots/"+bot.ID+"/channels", nil); strings.Contains(body, "qr-app") {
				t.Fatalf("the registration saved credentials after the archive: %s", body)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.startupRequests != 0 {
				t.Fatal("an archived bot started a Feishu channel")
			}
		})
	}
}
