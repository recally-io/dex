package app_test

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/db"
)

func TestUserRoutineDeliversToPairedWechatOwner(t *testing.T) {
	// Given a paired WeChat owner with a recent context token.
	f := newFakeWechat(t)
	d := startWechat(t, f, t.TempDir())
	connectWechat(t, d)
	f.batch("cursor", wxText(1, "owner", "hello"))
	if got := wxSentText(f.next(t)); got != "echo: hello" {
		t.Fatalf("initial reply=%q", got)
	}
	d.setModel(t, "tool")
	var targets struct {
		Targets []struct {
			ID string `json:"id"`
		} `json:"targets"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/notification-targets", nil, &targets)
	if len(targets.Targets) != 1 {
		t.Fatalf("targets=%+v", targets)
	}
	body := map[string]any{"name": "WeChat check", "prompt": `report_check {"outcome":"changed","summary":"wechat notification"}`, "schedule": map[string]any{"kind": "daily", "time": "09:00"}, "timeZone": "UTC", "session": "new", "enabled": true, "notification": map[string]any{"mode": "target", "targetId": targets.Targets[0].ID}}
	var routine map[string]any
	d.must(t, http.MethodPost, "/api/agents/default/automations", body, &routine, http.StatusCreated)
	// When the routine settles, the owner receives the summary with its context.
	run := d.runNow(t, routine["id"].(string))
	settledRoutine(t, d, routine["id"].(string), run.ID)
	got := f.next(t)
	if got.Msg.To != "owner" || got.Msg.Context != "context1" || !strings.Contains(wxSentText(got), "wechat notification") {
		t.Fatalf("delivery=%+v", got)
	}
}

func TestUserRoutineDeliversToPairedFeishuOwner(t *testing.T) {
	// Given a Feishu bot paired to its private owner.
	f := newFakeFeishu(t)
	d := startFeishu(t, f, t.TempDir())
	pairFeishu(t, d, f)
	d.setModel(t, "tool")
	var targets struct {
		Targets []struct {
			ID string `json:"id"`
		} `json:"targets"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/notification-targets", nil, &targets)
	if len(targets.Targets) != 1 {
		t.Fatalf("targets=%+v", targets)
	}
	body := map[string]any{"name": "Feishu check", "prompt": `report_check {"outcome":"changed","summary":"feishu notification"}`, "schedule": map[string]any{"kind": "daily", "time": "09:00"}, "timeZone": "UTC", "session": "new", "enabled": true, "notification": map[string]any{"mode": "target", "targetId": targets.Targets[0].ID}}
	var routine map[string]any
	d.must(t, http.MethodPost, "/api/agents/default/automations", body, &routine, http.StatusCreated)
	// When the routine settles, the platform receives its summary.
	run := d.runNow(t, routine["id"].(string))
	settledRoutine(t, d, routine["id"].(string), run.ID)
	f.waitReply(t, "feishu notification")
}

func TestUserRoutineNotificationRetriesAfterRestart(t *testing.T) {
	// Given a routine whose remote Telegram send fails temporarily.
	f := newFakeTelegram(t)
	f.failSend.Store(true)
	data := t.TempDir()
	d := startTelegram(t, f, data, 42)
	d.setModel(t, "tool")
	var targets struct {
		Targets []struct {
			ID string `json:"id"`
		} `json:"targets"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/notification-targets", nil, &targets)
	body := map[string]any{"name": "Retry", "prompt": `report_check {"outcome":"changed","summary":"retry result"}`, "schedule": map[string]any{"kind": "daily", "time": "09:00"}, "timeZone": "UTC", "session": "new", "enabled": true, "notification": map[string]any{"mode": "target", "targetId": targets.Targets[0].ID}}
	var routine map[string]any
	d.must(t, http.MethodPost, "/api/agents/default/automations", body, &routine, http.StatusCreated)
	run := d.runNow(t, routine["id"].(string))
	settledRoutine(t, d, routine["id"].(string), run.ID)
	select {
	case <-f.attempted:
	case <-time.After(15 * time.Second):
		t.Fatal("no delivery attempt")
	}
	// When Dex restarts and the platform accepts sends.
	d.stop()
	f.failSend.Store(false)
	startDex(t, data, f.srv.URL)
	// Then the saved pending result is delivered to the original destination.
	got := f.next(t)
	if got.ChatID != 42 || !strings.Contains(got.Text, "retry result") {
		t.Fatalf("delivery=%+v", got)
	}
}

func TestUserRoutineDeliversToAuthorizedTelegramOwner(t *testing.T) {
	// Given a connected Telegram bot and an authorized owner.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	d.setModel(t, "tool")
	var targets struct {
		Targets []struct {
			ID string `json:"id"`
		} `json:"targets"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/notification-targets", nil, &targets)
	if len(targets.Targets) != 1 {
		t.Fatalf("targets: %+v", targets)
	}
	body := map[string]any{"name": "Check", "prompt": `report_check {"outcome":"changed","summary":"fixture notification"}`, "schedule": map[string]any{"kind": "daily", "time": "09:00"}, "timeZone": "UTC", "session": "new", "enabled": true, "notification": map[string]any{"mode": "target", "targetId": targets.Targets[0].ID}}
	var routine map[string]any
	d.must(t, http.MethodPost, "/api/agents/default/automations", body, &routine, http.StatusCreated)
	// When the routine finishes.
	run := d.runNow(t, routine["id"].(string))
	settledRoutine(t, d, routine["id"].(string), run.ID)
	// Then its actual platform destination receives the summary.
	got := f.next(t)
	if got.ChatID != 42 || !strings.Contains(got.Text, "fixture notification") {
		t.Fatalf("delivery: %+v", got)
	}
}

func TestUserRoutineSettlementQueuesOneNotification(t *testing.T) {
	// Given a routine routed to a saved authorized destination.
	data := t.TempDir()
	d := startDexWith(t, app.Config{DataDir: data})
	d.setModel(t, "tool")
	conn, err := db.Open(t.Context(), filepath.Join(data, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err = conn.ExecContext(t.Context(), `INSERT INTO routine_notification_targets (id, agent_id, platform, account_id, peer) VALUES ('target','default','telegram','123','telegram:42')`); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"name": "Check", "prompt": `report_check {"outcome":"changed","summary":"new result"}`, "schedule": map[string]any{"kind": "daily", "time": "09:00"}, "timeZone": "UTC", "session": "new", "enabled": true, "notification": map[string]any{"mode": "target", "targetId": "target"}}
	var routine map[string]any
	d.must(t, http.MethodPost, "/api/agents/default/automations", body, &routine, http.StatusCreated)
	// When the real native routine finishes and reports its outcome.
	run := d.runNow(t, routine["id"].(string))
	settledRoutine(t, d, routine["id"].(string), run.ID)
	// Then exactly one local delivery is queued with the resolved target and summary.
	var count int
	var peer, text string
	if err = conn.QueryRowContext(t.Context(), `SELECT count(*), coalesce(max(peer),''), coalesce(max(body),'') FROM routine_notification_deliveries WHERE run_id=?`, run.ID).Scan(&count, &peer, &text); err != nil {
		t.Fatal(err)
	}
	if count != 1 || peer != "telegram:42" || !strings.Contains(text, "new result") {
		t.Fatalf("delivery count=%d peer=%q text=%q", count, peer, text)
	}
}

func TestUserCanReadEmptyNotificationTargets(t *testing.T) {
	// Given a bot without paired channels.
	d := startDex(t, t.TempDir(), "")
	// When the user asks which destinations can receive notifications.
	var got struct {
		Targets   []any  `json:"targets"`
		PrimaryID string `json:"primaryId"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/notification-targets", nil, &got)
	// Then no recipient is guessed.
	if got.Targets == nil || len(got.Targets) != 0 || got.PrimaryID != "" {
		t.Fatalf("unexpected destinations: %+v", got)
	}
}

func TestUserCanSetRoutineNotificationRoute(t *testing.T) {
	// Given a user creating a routine with an explicit default-channel route.
	d := startDex(t, t.TempDir(), "")
	body := map[string]any{"name": "Check", "prompt": "check", "schedule": map[string]any{"kind": "daily", "time": "09:00"}, "timeZone": "UTC", "session": "new", "enabled": true, "notification": map[string]any{"mode": "default"}}
	// When the user saves it via the existing routine API.
	var created map[string]any
	d.must(t, http.MethodPost, "/api/agents/default/automations", body, &created, http.StatusCreated)
	// Then the selected route is returned and persists on read.
	var fetched map[string]any
	d.must(t, http.MethodGet, "/api/agents/default/automations/"+created["id"].(string), nil, &fetched)
	for _, v := range []map[string]any{created, fetched} {
		notification, ok := v["notification"].(map[string]any)
		if !ok || notification["mode"] != "default" {
			t.Fatalf("notification route missing: %+v", v)
		}
	}
	// Older clients omit the field on update; they must not erase the route.
	delete(body, "notification")
	var updated map[string]any
	d.must(t, http.MethodPut, "/api/agents/default/automations/"+created["id"].(string), body, &updated)
	if updated["notification"].(map[string]any)["mode"] != "default" {
		t.Fatalf("omitted route changed: %+v", updated)
	}
	body["notification"] = map[string]any{"mode": "target", "targetId": "not-owned"}
	if status, _ := d.call(t, http.MethodPut, "/api/agents/default/automations/"+created["id"].(string), body); status != http.StatusBadRequest {
		t.Fatalf("unknown target accepted: %d", status)
	}
}
