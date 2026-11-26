package app_test

import (
	"encoding/json/v2"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/app"
	botpkg "github.com/recally-io/dex/internal/bot"
	"github.com/recally-io/dex/internal/settings"
	"github.com/recally-io/dex/internal/sysops"
)

func channelApprovalPath(t *testing.T, answer string) string {
	t.Helper()
	var result struct {
		Status       string `json:"status"`
		ApprovalPath string `json:"approvalPath"`
	}
	data, ok := strings.CutPrefix(answer, "tool: ")
	if !ok || json.Unmarshal([]byte(data), &result) != nil || result.Status != "pending_owner_confirmation" || !strings.HasPrefix(result.ApprovalPath, "/channel-approvals/") {
		t.Fatalf("expected a pending owner review, got %q", answer)
	}
	return result.ApprovalPath
}

func reviewChannelChange(t *testing.T, d *dex, client *http.Client, method, path, origin string, want int) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, d.srv.URL+path, strings.NewReader("confirm=yes"))
	if err != nil {
		t.Fatal(err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != want {
		t.Fatalf("review: got %d, want %d: %s (%v)", resp.StatusCode, want, data, err)
	}
	return string(data)
}

func TestUserManagesChannelsOnlyThroughOwnerReview(t *testing.T) {
	// Given a signed-in owner and a second bot with paired Telegram users.
	logs := sysops.NewLogBuffer(1000)
	old := slog.Default()
	slog.SetDefault(slog.New(sysops.TeeHandler(slog.DiscardHandler, logs)))
	t.Cleanup(func() { slog.SetDefault(old) })
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startDexWith(t, app.Config{DataDir: dir, TelegramAPIURL: f.srv.URL, RequireToken: true})
	token, err := settings.StoredToken(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	owner := d.srv.Client()
	owner.Jar = jar
	d.must(t, http.MethodPost, "/api/session", map[string]string{"token": token}, nil, http.StatusNoContent)
	bot := d.createCoder(t)
	base := "/api/bots/" + bot.ID
	setTelegram := func(users ...int64) {
		d.must(t, http.MethodPut, base+"/channels/telegram", map[string]any{"token": fakeToken, "allowedUsers": users}, nil)
	}
	setTelegram(42, 43)
	session := d.newSession(t).ID
	read := func(tool, botID string) string {
		t.Helper()
		answer := taskTool(t, d, session, tool, map[string]string{"bot": botID})
		if strings.Contains(answer, fakeToken) || strings.Contains(answer, token) {
			t.Fatal("channel tools exposed a credential")
		}
		return answer
	}
	if got := read("channel_status", bot.ID); !strings.Contains(got, `"telegram":"running"`) {
		t.Fatalf("Telegram status: %q", got)
	}
	if got := read("channel_pairing", bot.ID); !strings.Contains(got, `"telegram":true`) {
		t.Fatalf("Telegram pairing state: %q", got)
	}
	if got := read("channel_status", "default"); !strings.Contains(got, `"telegram":"off"`) {
		t.Fatalf("default bot status: %q", got)
	}
	got := read("channel_notification_targets", bot.ID)
	var targets botpkg.NotificationTargets
	if err := json.Unmarshal([]byte(strings.TrimPrefix(got, "tool: ")), &targets); err != nil || len(targets.Targets) != 2 {
		t.Fatalf("notification destinations: %q (%v)", got, err)
	}
	target := targets.Targets[1]
	request := func(botID, op, target string) string {
		t.Helper()
		return taskTool(t, d, session, "channel_request_change", map[string]string{"bot": botID, "operation": op, "target": target})
	}
	if got := request("default", "set_notification_target", target.ID); !strings.HasPrefix(got, "tool error:") {
		t.Fatalf("accepted a destination from another bot: %q", got)
	}

	// Reject an unexpected credential field without returning or displaying its value.
	const canary = "unexpected-credential-canary"
	invalid := taskTool(t, d, session, "channel_request_change", map[string]string{"operation": "disconnect", "bot": bot.ID, "target": "telegram", "token": canary})
	if !strings.Contains(invalid, "invalid channel tool arguments") || strings.Contains(invalid, canary) {
		t.Fatal("a rejected argument reached the tool result")
	}
	watch := d.watch(t, session)
	watch.until(t, "rejected change", answered(invalid))
	for _, item := range watch.tr.items {
		if item.Name == "channel_request_change" && (len(item.Args) != 0 || strings.Contains(item.Output, canary)) {
			t.Fatal("a rejected argument reached the tool transcript")
		}
	}

	// When the model asks for a change, session text and unauthenticated or cross-site POSTs cannot approve it.
	path := channelApprovalPath(t, request(bot.ID, "set_notification_target", target.ID))
	if got := taskTurn(t, d, session, `channel_request_change {"operation":"confirm","target":`+taskJSON(t, path)+`}`); !strings.HasPrefix(got, "tool error:") {
		t.Fatalf("the model approved its own request: %q", got)
	}
	guest := &http.Client{Transport: owner.Transport}
	reviewChannelChange(t, d, guest, http.MethodGet, path, "", http.StatusUnauthorized)
	reviewChannelChange(t, d, guest, http.MethodPost, path, "", http.StatusUnauthorized)
	reviewChannelChange(t, d, owner, http.MethodPost, path, "https://example.invalid", http.StatusForbidden)
	page := reviewChannelChange(t, d, owner, http.MethodGet, path, "", http.StatusOK)
	if !strings.Contains(page, target.Peer) || !strings.Contains(page, bot.ID) || !strings.Contains(page, "Set the primary notification destination") || strings.Contains(page, fakeToken) {
		t.Fatal("the review page does not show the exact redacted operation")
	}
	primary := func() string {
		var result botpkg.NotificationTargets
		d.must(t, http.MethodGet, base+"/notification-targets", nil, &result)
		return result.PrimaryID
	}
	if got := primary(); got != "" {
		t.Fatalf("changed primary before approval: %s", got)
	}

	// Then a signed-in approval applies only the stored destination, and cannot be replayed.
	reviewChannelChange(t, d, owner, http.MethodPost, path+"?target="+targets.Targets[0].ID, "", http.StatusOK)
	if got := primary(); got != target.ID {
		t.Fatalf("set the wrong primary: %s", got)
	}
	reviewChannelChange(t, d, owner, http.MethodPost, path, "", http.StatusConflict)
	clearPath := channelApprovalPath(t, request(bot.ID, "clear_notification_target", ""))
	reviewChannelChange(t, d, owner, http.MethodPost, clearPath, "", http.StatusOK)
	if got := primary(); got != "" {
		t.Fatalf("did not clear primary: %s", got)
	}

	// A revoked target cannot be selected. Reusing its ID for another allowed peer cannot revive the approval.
	removed := channelApprovalPath(t, request(bot.ID, "set_notification_target", target.ID))
	reused := channelApprovalPath(t, request(bot.ID, "set_notification_target", target.ID))
	setTelegram(42)
	reviewChannelChange(t, d, owner, http.MethodPost, removed, "", http.StatusConflict)
	setTelegram(42, 44)
	conn := sessionDB(t, d)
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO automation_notification_targets (id, bot_id, platform, account_id, peer)
		SELECT ?, bot_id, 'telegram', substr(token, 1, instr(token, ':') - 1), 'telegram:44' FROM telegram_settings WHERE bot_id = ?`, target.ID, bot.ID); err != nil {
		t.Fatal(err)
	}
	reviewChannelChange(t, d, owner, http.MethodPost, reused, "", http.StatusConflict)
	if got := primary(); got != "" {
		t.Fatalf("a stale approval selected a primary destination: %s", got)
	}

	// A change to the pairing invalidates the pending approval. A fresh approval revokes it.
	stale := channelApprovalPath(t, request(bot.ID, "revoke_pairing", "telegram"))
	setTelegram(44)
	reviewChannelChange(t, d, owner, http.MethodPost, stale, "", http.StatusConflict)
	// Owner approval changes pairing without resetting the saved webhook.
	hook := "/hooks/channels/telegram/" + bot.ID
	const hookSecret = "owner-review-webhook"
	d.must(t, http.MethodPut, base+"/channels/telegram", map[string]any{
		"token": "", "allowedUsers": []int64{44}, "transport": "webhook",
		"webhook": map[string]string{"url": "https://hooks.example.test" + hook, "secretToken": hookSecret},
	}, nil)
	f.nextEvent(t, "setWebhook")
	revoke := channelApprovalPath(t, request(bot.ID, "revoke_pairing", "telegram"))
	reviewChannelChange(t, d, owner, http.MethodPost, revoke, "", http.StatusOK)
	if got := read("channel_pairing", bot.ID); !strings.Contains(got, `"telegram":false`) {
		t.Fatalf("pairing survived revocation: %q", got)
	}
	f.nextEvent(t, "setWebhook")
	headers := map[string]string{"X-Telegram-Bot-Api-Secret-Token": hookSecret}
	postChannelHook(t, d, hook, []byte(`{"update_id":700}`), headers, http.StatusOK)
	var pairing struct {
		URL string `json:"url"`
	}
	d.must(t, http.MethodPost, base+"/channels/telegram/pairing", nil, &pairing)
	_, code, ok := strings.Cut(pairing.URL, "?start=")
	if !ok || code == "" || strings.Contains(read("channel_pairing", bot.ID), code) {
		t.Fatal("the pairing secret must only appear in the secure settings response")
	}
	disconnect := channelApprovalPath(t, request(bot.ID, "disconnect", "telegram"))
	reviewChannelChange(t, d, owner, http.MethodPost, disconnect, "", http.StatusOK)
	if got := read("channel_status", bot.ID); !strings.Contains(got, `"telegram":"off"`) {
		t.Fatalf("channel survived disconnection: %q", got)
	}
	f.nextEvent(t, "deleteWebhook")
	postChannelHook(t, d, hook, []byte(`{"update_id":700}`), headers, http.StatusNotFound)

	// The audit contains both outcomes and no credentials, peer IDs or confirmation links.
	var applied, denied, staleDenied int
	for _, entry := range logs.Entries(0) {
		if entry.Message != "channel tool mutation" {
			continue
		}
		if strings.Contains(entry.Attrs, fakeToken) || strings.Contains(entry.Attrs, token) || strings.Contains(entry.Attrs, target.Peer) || strings.Contains(entry.Attrs, path) || strings.Contains(entry.Attrs, code) || strings.Contains(entry.Attrs, canary) {
			t.Fatal("audit leaked a sensitive value")
		}
		if strings.Contains(entry.Attrs, "outcome=applied") {
			applied++
		}
		if strings.Contains(entry.Attrs, "outcome=denied") {
			denied++
			if strings.Contains(entry.Attrs, "operation=set_notification_target") && strings.Contains(entry.Attrs, "bot="+bot.ID) {
				staleDenied++
			}
		}
	}
	if applied != 4 || denied < 5 || staleDenied != 2 {
		t.Fatalf("audit: %d applied, %d denied, %d stale destinations denied", applied, denied, staleDenied)
	}
}

func TestUserRevokesFeishuPairingWithoutReplacingCredentials(t *testing.T) {
	t.Parallel()
	// Given a paired Feishu app.
	f := newFakeFeishu(t)
	d := startFeishu(t, f, t.TempDir())
	pairFeishu(t, d, f)
	session := d.newSession(t).ID
	// When the owner reviews and approves a request to revoke its pairing.
	answer := taskTool(t, d, session, "channel_request_change", map[string]string{"operation": "revoke_pairing", "target": "feishu"})
	path := channelApprovalPath(t, answer)
	reviewChannelChange(t, d, d.srv.Client(), http.MethodPost, path, "", http.StatusOK)
	// Then the app stays connected to settings, and the secure form can issue a new code without a new secret.
	var config botpkg.Feishu
	base := "/api/bots/default/channels/feishu"
	d.must(t, http.MethodGet, base, nil, &config)
	if !config.Enabled || config.Paired {
		t.Fatalf("Feishu settings after revocation: %+v", config)
	}
	var setup botpkg.FeishuSetup
	d.must(t, http.MethodPut, base, map[string]string{"appId": "app", "brand": "feishu"}, &setup)
	got := taskTool(t, d, session, "channel_pairing", map[string]string{})
	if setup.PairingCode == "" || !strings.Contains(got, `"feishu":false`) || strings.Contains(got, setup.PairingCode) || strings.Contains(got, "app-secret") {
		t.Fatal("the pairing secret must only appear on the secure setup surface")
	}
}

func TestUserCanRequestChannelChangesFromPrivateSessionButNotFromAChild(t *testing.T) {
	t.Parallel()
	// Given a paired Telegram sender and an owner who can delegate work.
	f := newFakeTelegram(t)
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	d.setModel(t, "direct")
	change := `channel_request_change {"operation":"disconnect","target":"telegram"}`
	// A private Telegram input can request the same review as a Web input.
	f.message(1, 42, 42, change)
	if got := f.next(t).Text; !strings.Contains(got, "pending_owner_confirmation") || !strings.Contains(got, "/channel-approvals/") {
		t.Fatalf("paired DM management result: %q", got)
	}
	// A child of a web owner cannot borrow the parent's authority.
	parent := newParent(t, d)
	spawnChild(t, d, parent, "Delegated request", change, "direct")
	spawnChild(t, d, parent, "Delegated read", "channel_notification_targets {}", "direct")
	for _, row := range waitResults(t, taskResultDB(t, dir), parent, 2) {
		got, _ := resultBody(t, row.content)["answer"].(string)
		if !strings.Contains(got, "tool error:") || strings.Contains(got, "approvalPath") || strings.Contains(got, "primaryId") {
			t.Fatalf("child management result: %q", got)
		}
	}
	if got := taskTool(t, d, parent, "channel_status", map[string]string{}); !strings.Contains(got, `"telegram":"running"`) {
		t.Fatalf("Telegram was changed: %q", got)
	}
}
