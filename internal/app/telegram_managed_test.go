package app_test

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/bot"
)

const managedSetupPath = "/api/bots/default/channels/telegram/managed"
const telegramManagerPath = "/api/telegram/manager"

func TestUserCanStartManagedTelegramOnlyWithManagementMode(t *testing.T) {
	t.Parallel()
	// Given a manager without Bot Management Mode.
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":456,"username":"DexManagerBot","is_bot":true,"can_manage_bots":false}}`))
	}))
	t.Cleanup(fake.Close)
	d := startDex(t, t.TempDir(), fake.URL)

	// When the owner configures the manager or starts setup, neither succeeds.
	for _, action := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, telegramManagerPath, map[string]string{"token": "456:managerSecret"}},
		{http.MethodPost, managedSetupPath, nil},
	} {
		if status, body := d.call(t, action.method, action.path, action.body); status != http.StatusBadRequest {
			t.Fatalf("%s %s = %d %s, want 400", action.method, action.path, status, body)
		}
	}
	// Then the existing-token flow remains usable.
	if status, body := d.call(t, http.MethodPut, "/api/bots/default/channels/telegram", map[string]any{"token": fakeToken, "allowedUsers": []int64{42}}); status != http.StatusOK {
		t.Fatalf("save existing token = %d %s", status, body)
	}
}

type managedTelegramAPI struct {
	mu         sync.Mutex
	updates    []map[string]any
	offset     atomic.Int64
	fetched    atomic.Int64
	childPolls atomic.Int64
	failToken  bool
	tokenGate  <-chan struct{}
	old        *fakeTelegram
	child      *fakeTelegram
	server     *httptest.Server
}

const managerTestToken = "456:managerSecret"
const managedTestToken = "789:managedSecret"
const telegramChannelPath = "/api/bots/default/channels/telegram"

func fakeManagedTelegram(t *testing.T, failToken bool) *managedTelegramAPI {
	t.Helper()
	f := &managedTelegramAPI{old: newFakeTelegram(t), child: newFakeTelegram(t), failToken: failToken}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *managedTelegramAPI) serve(w http.ResponseWriter, r *http.Request) {
	if method, ok := strings.CutPrefix(r.URL.Path, "/bot"+managerTestToken+"/"); ok {
		switch method {
		case "getMe":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":456,"username":"DexManagerBot","is_bot":true,"can_manage_bots":true}}`))
		case "getUpdates":
			var input struct {
				Offset  int64    `json:"offset"`
				Allowed []string `json:"allowed_updates"`
			}
			if json.UnmarshalRead(r.Body, &input) != nil || strings.Join(input.Allowed, ",") != "message,managed_bot" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.offset.Store(input.Offset)
			updates := []map[string]any{}
			f.mu.Lock()
			for _, update := range f.updates {
				if id, _ := update["update_id"].(int64); id >= input.Offset {
					updates = append(updates, update)
				}
			}
			f.mu.Unlock()
			_ = json.MarshalWrite(w, map[string]any{"ok": true, "result": updates})
		case "getManagedBotToken":
			var input struct {
				UserID int64 `json:"user_id"`
			}
			if json.UnmarshalRead(r.Body, &input) != nil || input.UserID != 789 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.fetched.Add(1)
			if f.tokenGate != nil {
				select {
				case <-f.tokenGate:
				case <-r.Context().Done():
					return
				}
			}
			if f.failToken {
				_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"789:managedSecret"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":"` + managedTestToken + `"}`))
		default:
			http.NotFound(w, r)
		}
		return
	}
	if method, ok := strings.CutPrefix(r.URL.Path, "/bot"+managedTestToken+"/"); ok {
		switch method {
		case "getMe":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":789,"is_bot":true}}`))
		case "setWebhook", "deleteWebhook", "setMyCommands", "setMessageReaction", "sendRichMessage":
			request := r.Clone(r.Context())
			request.URL.Path = "/bot" + fakeToken + "/" + method
			f.child.serve(w, request)
		case "getUpdates":
			_, _ = io.Copy(io.Discard, r.Body)
			f.childPolls.Add(1)
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
		return
	}
	f.old.serve(w, r)
}

func (f *managedTelegramAPI) push(id int64, key string, event map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, map[string]any{"update_id": id, key: event})
}

func (f *managedTelegramAPI) managed(id, owner int64, username string) {
	f.push(id, "managed_bot", map[string]any{
		"user": map[string]any{"id": owner, "is_bot": false},
		"bot":  map[string]any{"id": 789, "is_bot": true, "username": username},
	})
}

func configureManagedTelegram(t *testing.T, d *dex) {
	t.Helper()
	d.must(t, http.MethodPut, telegramManagerPath, map[string]string{"token": managerTestToken}, nil)
	var channels bot.Channels
	d.must(t, http.MethodGet, "/api/bots/default/channels", nil, &channels)
	if !channels.Telegram.ManagedCreationAvailable {
		t.Fatal("managed creation is not available")
	}
}

func beginAndVerifyManagedTelegram(t *testing.T, d *dex, f *managedTelegramAPI, updateID int64) (string, string) {
	t.Helper()
	var setup bot.TelegramManagedSetup
	d.must(t, http.MethodPost, managedSetupPath, nil, &setup)
	link, err := url.Parse(setup.URL)
	if err != nil || setup.State != "verify_owner" || link.Host != "t.me" || link.Path != "/DexManagerBot" || link.Query().Get("start") == "" {
		t.Fatalf("want a private owner verification link, got %+v (%v)", setup, err)
	}
	path := managedSetupPath + "/" + setup.ID
	f.push(updateID, "message", map[string]any{
		"from": map[string]any{"id": 42}, "chat": map[string]any{"type": "private"},
		"text": "/start " + link.Query().Get("start"),
	})
	waitManaged(t, func() bool {
		d.must(t, http.MethodGet, path, nil, &setup)
		return setup.State == "create_bot"
	})
	link, err = url.Parse(setup.URL)
	if err != nil || setup.Username == "" || link.Host != "t.me" || link.Path != "/newbot/DexManagerBot/"+setup.Username || link.Query().Get("name") != "Dex Bot" {
		t.Fatalf("want Telegram's newbot link, got %+v (%v)", setup, err)
	}
	return path, setup.Username
}

func TestUserConnectsManagedTelegramAndKeepsItAfterReplay(t *testing.T) {
	t.Parallel()
	// Given a manager with management mode, a saved channel and a verified owner.
	dir := t.TempDir()
	f := fakeManagedTelegram(t, false)
	d := startDex(t, dir, f.server.URL)
	d.must(t, http.MethodPut, telegramChannelPath, map[string]any{"token": fakeToken, "allowedUsers": []int64{11}, "options": map[string]bool{"silent": true}}, nil)
	configureManagedTelegram(t, d)
	path, username := beginAndVerifyManagedTelegram(t, d, f, 1)

	// When Telegram confirms creation and delivers the same update twice.
	f.managed(2, 42, username)
	f.managed(2, 42, username)
	waitManaged(t, func() bool { return f.offset.Load() >= 3 && f.childPolls.Load() > 0 })

	// Then Dex polls the new bot once configured for the verified owner. The API returns no token.
	var setup bot.TelegramManagedSetup
	d.must(t, http.MethodGet, path, nil, &setup)
	if setup.State != "connected" || f.fetched.Load() != 1 {
		t.Fatalf("setup = %+v, token requests = %d", setup, f.fetched.Load())
	}
	assertManagedChannel := func(d *dex) {
		var channels bot.Channels
		d.must(t, http.MethodGet, "/api/bots/default/channels", nil, &channels)
		if tg := channels.Telegram; tg.BotID != "789" || len(tg.AllowedUsers) != 1 || tg.AllowedUsers[0] != 42 || !tg.Options.Silent {
			t.Fatalf("wrong managed channel %+v", tg)
		}
		for _, p := range []string{path, "/api/bots/default/channels", "/api/bots/default/vault"} {
			if _, raw := d.call(t, http.MethodGet, p, nil); strings.Contains(raw, managedTestToken) || strings.Contains(raw, managerTestToken) {
				t.Fatalf("API %s exposed a credential", p)
			}
		}
	}
	assertManagedChannel(d)
	// Editing with a blank token must retain the vault reference, including across restart.
	d.must(t, http.MethodPut, telegramChannelPath, map[string]any{"token": "", "allowedUsers": []int64{42}}, nil)
	var stored string
	if err := controlWriteDB(t, dir).QueryRowContext(t.Context(), "SELECT token FROM telegram_settings WHERE bot_id='default'").Scan(&stored); err != nil || stored == managedTestToken {
		t.Fatalf("managed credential must not be stored in SQLite: %v", err)
	}
	d.stop()
	priorPolls := f.childPolls.Load()
	f.managed(3, 42, username)
	d = startDex(t, dir, f.server.URL)
	waitManaged(t, func() bool { return f.offset.Load() >= 4 && f.childPolls.Load() > priorPolls })
	assertManagedChannel(d)
	// A subsequent setup cannot claim the same bot under a different username.
	path2, username2 := beginAndVerifyManagedTelegram(t, d, f, 4)
	if username2 == username {
		t.Fatal("setup links must have distinct usernames")
	}
	f.managed(5, 42, username2)
	waitManaged(t, func() bool { return f.offset.Load() >= 6 })
	setup = bot.TelegramManagedSetup{}
	d.must(t, http.MethodGet, path2, nil, &setup)
	if setup.State != "create_bot" {
		t.Fatalf("a second setup claimed the same bot: %+v", setup)
	}
	if f.fetched.Load() != 1 {
		t.Fatalf("replay fetched the token again: %d", f.fetched.Load())
	}
}

func TestUserKeepsTelegramWebhookWhenConnectingManagedBot(t *testing.T) {
	t.Parallel()
	// Given an existing webhook channel and a verified setup owner.
	dir := t.TempDir()
	f := fakeManagedTelegram(t, false)
	d := startDex(t, dir, f.server.URL)
	d.setModel(t, "echo")
	const secret = "managed-webhook-secret"
	const endpoint = "https://hooks.example.test" + telegramHook
	d.must(t, http.MethodPut, telegramChannelPath, map[string]any{
		"token": fakeToken, "allowedUsers": []int64{11}, "transport": "webhook",
		"webhook": map[string]string{"url": endpoint, "secretToken": secret},
	}, nil)
	f.old.nextEvent(t, "setWebhook")
	configureManagedTelegram(t, d)
	path, username := beginAndVerifyManagedTelegram(t, d, f, 1)

	// When Telegram confirms the new bot, the old webhook is removed and the new bot uses its settings.
	f.managed(2, 42, username)
	waitManaged(t, func() bool { return f.offset.Load() >= 3 })
	status, view := d.call(t, http.MethodGet, "/api/bots/default/channels", nil)
	if status != http.StatusOK || !strings.Contains(view, `"transport":"webhook"`) || !strings.Contains(view, endpoint) ||
		strings.Contains(view, secret) || strings.Contains(view, managedTestToken) {
		t.Fatalf("managed channel did not retain a write-only webhook configuration (HTTP %d)", status)
	}
	var setup bot.TelegramManagedSetup
	d.must(t, http.MethodGet, path, nil, &setup)
	if setup.State != "connected" {
		t.Fatalf("managed setup = %s", setup.State)
	}
	f.old.nextEvent(t, "deleteWebhook")
	f.child.nextEvent(t, "setWebhook")

	// Then a token-free edit and a restart keep webhook intake on the new account.
	d.must(t, http.MethodPut, telegramChannelPath, map[string]any{"token": "", "allowedUsers": []int64{42}}, nil)
	f.child.nextEvent(t, "setWebhook")
	d.stop()
	d = startDex(t, dir, f.server.URL)
	f.child.nextEvent(t, "setWebhook")
	const header = "X-Telegram-Bot-Api-Secret-Token"
	payload := telegramHookMessage(t, 201, 42, "managed webhook")
	postChannelHook(t, d, telegramHook, payload, map[string]string{header: "wrong"}, http.StatusUnauthorized)
	postChannelHook(t, d, telegramHook, payload, map[string]string{header: secret}, http.StatusOK)
	waitTaskChannelText(t, f.child, "echo: managed webhook")
	assertWebhookInputs(t, dir, "telegram:789:201", 1)
	if polls := f.childPolls.Load(); polls != 0 {
		t.Fatalf("managed bot polled %d times in webhook mode", polls)
	}
}

func TestUserKeepsTelegramWebhookWhenManagedBotCannotRemoveOldRegistration(t *testing.T) {
	t.Parallel()
	// Given a registered webhook that Telegram will not remove.
	f := fakeManagedTelegram(t, false)
	d := startDex(t, t.TempDir(), f.server.URL)
	d.setModel(t, "echo")
	const secret = "original-webhook-secret"
	d.must(t, http.MethodPut, telegramChannelPath, map[string]any{
		"token": fakeToken, "allowedUsers": []int64{11}, "transport": "webhook",
		"webhook": map[string]string{"url": "https://hooks.example.test" + telegramHook, "secretToken": secret},
	}, nil)
	f.old.nextEvent(t, "setWebhook")
	f.old.mu.Lock()
	f.old.failures["deleteWebhook"] = []platformFailure{{HTTPStatus: http.StatusServiceUnavailable}}
	f.old.mu.Unlock()
	configureManagedTelegram(t, d)
	path, username := beginAndVerifyManagedTelegram(t, d, f, 1)

	// When Telegram confirms creation, replacement fails and the original bot still answers.
	f.managed(2, 42, username)
	waitManaged(t, func() bool { return f.offset.Load() >= 3 })
	var setup bot.TelegramManagedSetup
	d.must(t, http.MethodGet, path, nil, &setup)
	if setup.State != "failed" {
		t.Fatalf("managed setup = %s, want failed", setup.State)
	}
	postChannelHook(t, d, telegramHook, telegramHookMessage(t, 201, 11, "original webhook"),
		map[string]string{"X-Telegram-Bot-Api-Secret-Token": secret}, http.StatusOK)
	waitTaskChannelText(t, f.old, "echo: original webhook")
}

func TestUserKeepsExistingTelegramWhenManagedSetupDoesNotComplete(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name, state string
		owner       int64
		fetches     int64
	}{
		{"wrong creator", "create_bot", 99, 0},
		{"renamed bot", "create_bot", 42, 0},
		{"expired", "expired", 42, 0},
		{"canceled", "canceled", 42, 0},
		{"canceled during token request", "canceled", 42, 1},
		{"manager removed", "canceled", 42, 0},
		{"failed token request", "failed", 42, 1},
		{"manual save supersedes setup", "canceled", 42, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			// Given an active existing bot and an owner verification for its replacement.
			dir := t.TempDir()
			f := fakeManagedTelegram(t, scenario.state == "failed")
			var gate chan struct{}
			if scenario.name == "canceled during token request" {
				gate = make(chan struct{})
				f.tokenGate = gate
			}
			d := startDex(t, dir, f.server.URL)
			d.setModel(t, "echo")
			manual := map[string]any{"token": fakeToken, "allowedUsers": []int64{11}}
			d.must(t, http.MethodPut, telegramChannelPath, manual, nil)
			configureManagedTelegram(t, d)
			path, username := beginAndVerifyManagedTelegram(t, d, f, 1)
			// When this setup cannot accept the next managed_bot update.
			switch scenario.name {
			case "renamed bot":
				username = "SomeOtherBot"
			case "expired":
				_, err := controlWriteDB(t, dir).ExecContext(t.Context(), "UPDATE telegram_managed_setups SET expires_at=0")
				if err != nil {
					t.Fatal(err)
				}
			case "canceled":
				d.must(t, http.MethodDelete, path, nil, nil)
			case "manual save supersedes setup":
				d.must(t, http.MethodPut, telegramChannelPath, manual, nil)
			case "manager removed":
				d.must(t, http.MethodPut, telegramManagerPath, map[string]string{"token": ""}, nil)
			}
			f.managed(2, scenario.owner, username)
			if gate != nil {
				waitManaged(t, func() bool { return f.fetched.Load() == 1 })
				d.must(t, http.MethodDelete, path, nil, nil)
				close(gate)
			}
			if scenario.name != "manager removed" {
				waitManaged(t, func() bool { return f.offset.Load() >= 3 })
			}
			// Then the existing channel still answers and no rejected update fetches a token.
			var setup bot.TelegramManagedSetup
			d.must(t, http.MethodGet, path, nil, &setup)
			if setup.State != scenario.state || f.fetched.Load() != scenario.fetches {
				t.Fatalf("setup = %+v, token requests = %d", setup, f.fetched.Load())
			}
			f.old.message(1, 11, 11, "existing bot still works")
			if reply := f.old.next(t); reply.Text != "echo: existing bot still works" {
				t.Fatalf("existing channel reply = %+v", reply)
			}
		})
	}
}

func waitManaged(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for range time.Tick(20 * time.Millisecond) {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Telegram setup did not reach the expected state")
		}
	}
}
