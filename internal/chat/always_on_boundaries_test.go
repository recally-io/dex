package chat

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserSystemTurnToolsRejectBotMutations(t *testing.T) {
	r := startNative(t, "echo")
	main, err := r.service.Main(t.Context(), "bot")
	if err != nil {
		t.Fatal(err)
	}
	userID, err := r.service.Post(t.Context(), "bot", MainKey, "", "u-root", "hello")
	if err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "u-root")
	conv, err := r.service.q.GetConversation(t.Context(), userID)
	if err != nil {
		t.Fatal(err)
	}

	// Inject both a monitor System Turn and a background wake System Turn.
	for _, tc := range []struct {
		id     string
		origin string
		kind   string
	}{
		{id: "monitor:t1:fire", origin: "monitor", kind: "task_result"},
		{id: "background:b1:end", origin: "task", kind: "task_result"},
	} {
		if _, err := r.service.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{
			ID:             tc.id,
			ConversationID: main.ID,
			Content:        "system event",
			Attachments:    "null",
			Origin:         tc.origin,
			Producer:       "plugin",
			Kind:           tc.kind,
			CreatedAt:      r.service.now().UnixMilli(),
		}); err != nil {
			t.Fatal(err)
		}
		call := engine.TaskContext{
			AgentID:         "bot",
			ConversationID:  conv.ExecutionID.Int64,
			SourceRequestID: tc.id,
			Key:             "bot/mut-" + tc.id,
		}
		if err := r.service.ToolBotCaller(t.Context(), call, true); err == nil {
			t.Fatalf("%s: expected error for bot mutation from system turn, got nil", tc.id)
		}
	}
}

func TestUserMonitorFireCommitBeforeWakeRecoversOnce(t *testing.T) {
	r := startNative(t, "echo")
	main, err := r.service.Main(t.Context(), "bot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.Post(t.Context(), "bot", MainKey, "", "u-monitor-rec", "start"); err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "u-monitor-rec")
	conv, err := r.service.q.GetConversation(t.Context(), main.ID)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	r.service.now = func() time.Time { return now }
	call := engine.TaskContext{
		AgentID:         "bot",
		ConversationID:  conv.ExecutionID.Int64,
		SourceRequestID: "u-monitor-rec",
		Key:             "bot/t-rec-1",
	}
	info, err := r.service.ToolMonitorSet(t.Context(), call, MonitorSetArgs{DelaySeconds: 120, Prompt: "recover once"})
	if err != nil {
		t.Fatal(err)
	}

	// Advance clock past due_at and fire the monitor.
	now = now.Add(3 * time.Minute)
	if err := r.service.FireDueMonitors(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Re-running FireDueMonitors must not double-fire or create a second submission.
	if err := r.service.FireDueMonitors(t.Context()); err != nil {
		t.Fatal(err)
	}
	row, err := r.service.q.GetMonitor(t.Context(), strings.TrimPrefix(info.ID, "schedule-"))
	if err != nil {
		t.Fatal(err)
	}
	if row.State != "fired" || !row.FireSubmissionID.Valid {
		t.Fatalf("expected fired monitor with submission ID, got %+v", row)
	}

	// Simulate crash before native admission settled by checking that reconcile settles it exactly once,
	// and produces zero unsolicited outbox rows (fail-quiet System Turn).
	if err := r.service.reconcileSession(t.Context(), row.ConversationID, ""); err != nil {
		t.Fatal(err)
	}
	sub := awaitReceipt(t, r, row.FireSubmissionID.String)
	if sub.Status != "done" {
		t.Fatalf("expected monitor submission done, got %+v", sub)
	}
	var outboxCount int
	if err := r.conn.QueryRowContext(t.Context(), `SELECT count(*) FROM outbox_messages WHERE source_submission_id = ?`, row.FireSubmissionID.String).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 0 {
		t.Fatalf("expected 0 outbox messages for quiet monitor turn, got %d", outboxCount)
	}
}

func TestUserTopLevelLostAdmissionReceiptReconcilesWithoutDuplicateOutbox(t *testing.T) {
	r := startNative(t, "echo")
	chatID, err := r.service.Post(t.Context(), "bot", MainKey, "", "u-lost-admit", "ping")
	if err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "u-lost-admit")

	// Simulate a crash between engine.Submit and markAdmitted by resetting admitted_at=NULL and status='pending'
	// on an already-settled native submission, then reconciling its Session.
	if _, err := r.conn.ExecContext(t.Context(), `UPDATE submissions SET status = 'pending', admitted_at = NULL, settled_at = NULL, answer = NULL WHERE id = 'u-lost-admit'`); err != nil {
		t.Fatal(err)
	}
	if err := r.service.reconcileSession(t.Context(), chatID, ""); err != nil {
		t.Fatal(err)
	}
	sub := awaitReceipt(t, r, "u-lost-admit")
	if sub.Status != "done" {
		t.Fatalf("expected reconciled submission done, got %+v", sub)
	}
	var outboxCount int
	if err := r.conn.QueryRowContext(t.Context(), `SELECT count(*) FROM outbox_messages WHERE source_submission_id = 'u-lost-admit'`).Scan(&outboxCount); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if outboxCount != 0 {
		t.Fatalf("Web input must not write outbox rows after lost admission reconcile, got %d", outboxCount)
	}
}
