package chat

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserSystemTurnFailQuietAndErrorFallback(t *testing.T) {
	r := startNative(t, "echo")
	conv, _ := taskParent(t, r)

	// Given a done System Turn (task_result) without notify_user or ask_user.
	if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{
		ID: "sys-quiet-1", ConversationID: conv, Content: "task result payload",
		Attachments: "null", Origin: "telegram:42", Kind: "task_result", CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.service.admit(t.Context(), taskInput(t, r, "sys-quiet-1")); err != nil {
		t.Fatal(err)
	}
	got := awaitReceipt(t, r, "sys-quiet-1")
	if got.Status != "done" {
		t.Fatalf("expected done system turn, got %+v", got)
	}
	var count int
	if err := r.conn.QueryRowContext(t.Context(), `SELECT count(*) FROM outbox_messages WHERE source_submission_id=?`, "sys-quiet-1").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("fail-quiet system turn must write 0 outbox rows, got %d", count)
	}

	// When a System Turn settles with an unhandled failure (e.g. model_error).
	if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{
		ID: "sys-fail-1", ConversationID: conv, Content: "task result payload",
		Attachments: "null", Origin: "telegram:42", Kind: "task_result", CreatedAt: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.service.settle(t.Context(), "sys-fail-1", engine.Outcome{Status: "unanswered", Reason: "model_error"}); err != nil {
		t.Fatal(err)
	}
	var kind, peer, body string
	if err := r.conn.QueryRowContext(t.Context(), `SELECT kind, peer, body FROM outbox_messages WHERE dedup_key=?`, "error:sys-fail-1").Scan(&kind, &peer, &body); err != nil {
		t.Fatal(err)
	}
	if kind != "system_error" || peer != "telegram:42" || !strings.Contains(body, "model_error") {
		t.Fatalf("unexpected system_error outbox row: kind=%q peer=%q body=%q", kind, peer, body)
	}
}

func TestUserNotifyUserSupersedesPendingTopicAndRecentOutbox(t *testing.T) {
	r := startNative(t, "echo")
	conv, _ := taskParent(t, r)
	if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{
		ID: "sys-notify-src", ConversationID: conv, Content: "wake",
		Attachments: "null", Origin: "telegram:42", Kind: "task_result", CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	row, err := r.q.GetConversation(t.Context(), conv)
	if err != nil {
		t.Fatal(err)
	}
	if !row.ExecutionID.Valid {
		// Ensure execution exists for toolCaller.
		if _, err := r.service.Post(t.Context(), "bot", MainKey, "", "seed-exec", "hello"); err != nil {
			t.Fatal(err)
		}
		if err := r.service.admit(t.Context(), taskInput(t, r, "seed-exec")); err != nil {
			t.Fatal(err)
		}
		awaitReceipt(t, r, "seed-exec")
		row, err = r.q.GetConversation(t.Context(), conv)
		if err != nil {
			t.Fatal(err)
		}
	}
	call := engine.TaskContext{
		AgentID: "bot", ConversationID: row.ExecutionID.Int64,
		SourceRequestID: "sys-notify-src", RunInputIDs: []string{"sys-notify-src"}, Key: "bot/101",
	}
	nonFinal := false
	firstID, err := r.service.ToolNotifyUser(t.Context(), call, engine.NotifyUserArgs{Content: "step 1", TopicKey: "deploy", Final: &nonFinal})
	if err != nil {
		t.Fatal(err)
	}
	call.Key = "bot/102"
	secondID, err := r.service.ToolNotifyUser(t.Context(), call, engine.NotifyUserArgs{Content: "step 2", TopicKey: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	firstMsg, err := r.q.GetOutboxMessage(t.Context(), firstID)
	if err != nil {
		t.Fatal(err)
	}
	secondMsg, err := r.q.GetOutboxMessage(t.Context(), secondID)
	if err != nil {
		t.Fatal(err)
	}
	if firstMsg.Status != "superseded" || secondMsg.Status != "pending" {
		t.Fatalf("expected first superseded and second pending, got %s / %s", firstMsg.Status, secondMsg.Status)
	}
	if err := r.q.MarkOutboxDelivered(t.Context(), sqlc.MarkOutboxDeliveredParams{
		ID: secondID, DeliveredAt: sql.NullInt64{Int64: r.service.now().UnixMilli(), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	recent, err := r.service.ToolRecentOutbox(t.Context(), engine.Ref{AgentID: "bot", ID: row.ExecutionID.Int64})
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) == 0 || recent[0].ID != secondID || recent[0].Body != "step 2" {
		t.Fatalf("unexpected recent outbox: %+v", recent)
	}
}

func TestUserChildTurnKeepsSavedSilentExit(t *testing.T) {
	r := startNative(t, "echo")
	ctx := t.Context()
	_, source := taskParent(t, r)
	if _, err := r.service.ToolSessionNew(ctx, source, engine.SessionNewArgs{Title: "Worker", Message: "Finish this turn"}); err != nil {
		t.Fatal(err)
	}
	// Given a child turn with a silent exit saved by an older release.
	turn := turnID(source.Key)
	if err := r.q.SetSubmissionExitAction(ctx, sqlc.SetSubmissionExitActionParams{ID: turn, ExitAction: "stay_silent"}); err != nil {
		t.Fatal(err)
	}

	// When the current release settles the turn, the parent gets no result.
	if err := r.service.admit(ctx, taskInput(t, r, turn)); err != nil {
		t.Fatal(err)
	}
	if got := awaitReceipt(t, r, turn); got.Status != "done" {
		t.Fatalf("child did not complete: %s", got.Status)
	}
	if _, err := r.q.GetSubmission(ctx, "result:"+turn); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("saved silent exit must not send a child result: %v", err)
	}
}

func TestUserChildReportsToParentFromSystemTurn(t *testing.T) {
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	child, err := r.service.ToolSessionNew(t.Context(), source, engine.SessionNewArgs{Title: "Worker", Message: "init"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.service.admit(t.Context(), taskInput(t, r, turnID(source.Key))); err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, turnID(source.Key))

	// Child System Turn can explicitly escalate to parent via its session_send (ToolReportParent).
	var childID int64
	if err := r.conn.QueryRowContext(t.Context(), `SELECT id FROM conversations WHERE parent_id = ?`, parent).Scan(&childID); err != nil {
		t.Fatal(err)
	}
	cRow, err := r.q.GetConversation(t.Context(), childID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{
		ID: "child-bg-wake", ConversationID: childID, Content: "bg done",
		Attachments: "null", Origin: "web", Kind: "task_result", CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	resultID, err := r.service.ToolReportParent(t.Context(), engine.TaskContext{
		AgentID: "bot", ConversationID: cRow.ExecutionID.Int64,
		SourceRequestID: "child-bg-wake", RunInputIDs: []string{"child-bg-wake"}, Key: "bot/202",
	}, engine.ReportParentArgs{Content: "explicit child escalation"})
	if err != nil {
		t.Fatal(err)
	}
	_ = child
	sub, err := r.q.GetSubmission(t.Context(), resultID)
	if err != nil {
		t.Fatal(err)
	}
	if sub.ConversationID != parent || sub.Kind != "task_result" || !strings.Contains(sub.Content, "explicit child escalation") {
		t.Fatalf("unexpected parent report submission: %+v", sub)
	}
}
