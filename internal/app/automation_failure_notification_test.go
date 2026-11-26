package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/api"
)

func createFailureRoutine(t *testing.T, d *dex, mode, target string) (string, map[string]any) {
	t.Helper()
	route := map[string]any{"mode": mode}
	if target != "" {
		route["targetId"] = target
	}
	body := map[string]any{"name": "Failure fixture", "prompt": "check", "schedule": map[string]any{"kind": "daily", "time": "09:00"}, "timeZone": "UTC", "session": "new", "enabled": false, "notification": route}
	var a automationInfo
	d.must(t, http.MethodPost, "/api/agents/default/automations", body, &a, http.StatusCreated)
	return a.ID, body
}

func telegramRoutineTarget(t *testing.T, d *dex) string {
	t.Helper()
	var targets struct {
		Targets []struct {
			ID string `json:"id"`
		} `json:"targets"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/notification-targets", nil, &targets)
	if len(targets.Targets) != 1 {
		t.Fatalf("targets=%+v", targets)
	}
	return targets.Targets[0].ID
}

func TestUserAutomationNotifiesWhenItCannotStart(t *testing.T) {
	t.Parallel()
	// Given an existing bot Main and an authorized default target whose fake send stays pending.
	f := newFakeTelegram(t)
	f.failSend.Store(true)
	d := startTelegram(t, f, t.TempDir(), 42)
	main := d.mainChat(t)
	d.must(t, http.MethodPut, "/api/agents/default/notification-targets", map[string]any{"targetId": telegramRoutineTarget(t, d)}, nil)
	d.setModel(t, "")
	id, _ := createFailureRoutine(t, d, "default", "")
	conn := sessionDB(t, d)
	// When the bot has no configured model, two Run Now requests both end before admission.
	first, second := d.runNow(t, id), d.runNow(t, id)
	if first.Status != "skipped" || second.Status != "skipped" || first.ChatID != 0 || second.ChatID != 0 {
		t.Fatalf("expected two skips without a routine chat: %+v, %+v", first, second)
	}
	// Then only the first run queues a message, with the existing Main only as its Outbox owner.
	var peer, status, body string
	var conversation int64
	var source sql.NullString
	if err := conn.QueryRowContext(t.Context(), `SELECT peer, status, body, conversation_id, source_submission_id FROM outbox_messages WHERE id=?`, "automation:"+first.ID).Scan(&peer, &status, &body, &conversation, &source); err != nil {
		t.Fatalf("first skipped run did not queue its alert: %v", err)
	}
	if peer != "telegram:42" || status != "pending" || !strings.Contains(body, "the bot has no model") || conversation != main || source.Valid {
		t.Fatalf("pending alert: peer=%q status=%q body=%q conversation=%d source=%v", peer, status, body, conversation, source)
	}
	assertAutomationOutbox(t, conn, second.ID, false)
	select {
	case <-f.attempted:
	case <-time.After(15 * time.Second):
		t.Fatal("the channel fixture did not see a delivery attempt")
	}
	var n int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM submissions WHERE conversation_id = ?`, main).Scan(&n); err != nil || n != 0 {
		t.Fatalf("Main received %d inputs: %v", n, err)
	}
	w := d.watch(t, main)
	w.until(t, "Main's notification snapshot", func(tr *transcript) bool { return len(tr.noticeEvents) > 0 })
	if len(w.tr.noticeEvents[0].Notifications) != 0 {
		t.Fatalf("Routine alert leaked into Main: %+v", w.tr.noticeEvents)
	}
	var outbox api.OutboxMessageList
	d.must(t, http.MethodGet, chatPath(main, "outbox"), nil, &outbox)
	if !slices.ContainsFunc(outbox.Messages, func(m api.OutboxMessage) bool {
		return m.Id == "automation:"+first.ID && m.Body == body && m.Status == api.OutboxMessageStatusPending
	}) {
		t.Fatalf("Main's Outbox did not retain the pending alert: %+v", outbox)
	}
}

func assertAutomationOutbox(t *testing.T, conn *sql.DB, runID string, want bool) {
	t.Helper()
	var exists bool
	if err := conn.QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM outbox_messages WHERE id=?)`, "automation:"+runID).Scan(&exists); err != nil || exists != want {
		t.Fatalf("run %s alert exists=%v, want %v (err=%v)", runID, exists, want, err)
	}
}

func TestUserAutomationFailureAlertsCompareOnlyThePreviousFinishedRun(t *testing.T) {
	t.Parallel()
	// Given a routine using an explicit authorized target and the faux automation_report model.
	f := newFakeTelegram(t)
	f.failSend.Store(true)
	d := startTelegram(t, f, t.TempDir(), 42)
	d.setModel(t, "tool")
	id, body := createFailureRoutine(t, d, "target", telegramRoutineTarget(t, d))
	body["session"] = "reuse"
	conn := sessionDB(t, d)
	var alertIDs []string
	var routineChat int64
	for _, step := range []struct {
		outcome, summary, key    string
		alert, businessDuplicate bool
	}{
		{"failed", "Timed out", "one", true, false},
		{"failed", "Timed out", "two", false, false},
		{"failed", "timed out", "one", true, true},
		{"failed", "Timed out", "one", true, true},
		{"unchanged", "healthy", "", false, false},
		{"failed", "Timed out", "one", true, true},
	} {
		// When the model reports each result in order.
		body["prompt"] = fmt.Sprintf(`automation_report {"outcome":%q,"summary":%q,"changeKey":%q}`, step.outcome, step.summary, step.key)
		d.must(t, http.MethodPut, "/api/agents/default/automations/"+id, body, nil)
		run := settledRoutine(t, d, id, d.runNow(t, id).ID)
		// Then the alert decision is independent of automation_report business deduplication.
		if run.Outcome != step.outcome || run.Summary != step.summary || run.Duplicate != step.businessDuplicate {
			t.Fatalf("result: %+v, want %+v", run, step)
		}
		assertAutomationOutbox(t, conn, run.ID, step.alert)
		if step.alert {
			alertIDs = append(alertIDs, run.ID)
			routineChat = run.ChatID
		}
	}
	// The owner can review the pending deliveries from their read-only Routine transcript.
	var outbox api.OutboxMessageList
	d.must(t, http.MethodGet, chatPath(routineChat, "outbox"), nil, &outbox)
	for _, id := range alertIDs {
		if !slices.ContainsFunc(outbox.Messages, func(m api.OutboxMessage) bool {
			return m.Id == "automation:"+id && m.Status == api.OutboxMessageStatusPending
		}) {
			t.Fatalf("Routine Outbox did not show pending alert %s: %+v", id, outbox)
		}
	}
	// Pending failure alerts do not replace each other even in a reused chat.
	for _, id := range alertIDs {
		var status string
		if err := conn.QueryRowContext(t.Context(), `SELECT status FROM outbox_messages WHERE id=?`, "automation:"+id).Scan(&status); err != nil || status != "pending" {
			t.Fatalf("alert %s must still be pending: %q (%v)", id, status, err)
		}
	}
}

func TestUserAutomationRunListShowsWhenNoNotificationTargetResolved(t *testing.T) {
	t.Parallel()
	// Given a bot without a primary target, including routines with an explicit opt-out.
	d := startDex(t, t.TempDir(), "")
	conn := sessionDB(t, d)
	for _, mode := range []string{"default", "none"} {
		t.Run(mode, func(t *testing.T) {
			id, _ := createFailureRoutine(t, d, mode, "")
			// When the run fails before its model starts.
			run := d.runNow(t, id)
			var list struct {
				Runs []map[string]any `json:"runs"`
			}
			d.must(t, http.MethodGet, "/api/agents/default/automations/"+id+"/runs", nil, &list)
			// Then the run list shows both the failure and why no message was sent.
			if len(list.Runs) != 1 || list.Runs[0]["reason"] != "the bot has no model" || list.Runs[0]["notificationNote"] != "not delivered: no notification target" {
				t.Fatalf("missing run notification note: %+v", list.Runs)
			}
			assertAutomationOutbox(t, conn, run.ID, false)
		})
	}
}
