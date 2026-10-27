package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func taskParent(t *testing.T, r *nativeTest) (int64, engine.TaskContext) {
	t.Helper()
	c, err := r.service.Main(t.Context(), "bot")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := r.service.execution(t.Context(), c.ID, "bot", engine.Options{Model: engine.ModelRef{Provider: "faux", ModelID: "echo"}, ThinkingLevel: "off"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: "source", ConversationID: c.ID, Content: "delegate", Attachments: "null", Origin: "web", Kind: "message", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID, engine.TaskContext{AgentID: "bot", ConversationID: ref.ID, SourceRequestID: "source", Key: "bot/spawn-1"}
}
func taskInput(t *testing.T, r *nativeTest, id string) sqlc.ListPendingSubmissionsRow {
	t.Helper()
	rows, err := r.q.ListPendingSubmissions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("missing pending input %s", id)
	return sqlc.ListPendingSubmissionsRow{}
}

func TestUserTaskReplayKeepsIdentityAndQueuesBeyondRunningLimit(t *testing.T) {
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	first, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "One", Goal: "one", Constraints: []string{"Do not change code"}, DoneCriteria: []string{"Explain the cause"}})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Replay", Goal: "different"})
	if err != nil || replay.ID != first.ID || replay.Goal != first.Goal || len(replay.Constraints) != 1 || replay.Constraints[0] != "Do not change code" || len(replay.DoneCriteria) != 1 || replay.DoneCriteria[0] != "Explain the cause" {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	source.Key = "bot/spawn-2"
	second, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Two", Goal: "two"})
	if err != nil {
		t.Fatal(err)
	}
	source.Key = "bot/spawn-3"
	third, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Three", Goal: "three"})
	if err != nil || third.Status != "queued" {
		t.Fatalf("queued third task: %+v %v", third, err)
	}
	cancelled, err := r.service.CancelTask(t.Context(), "bot", parent, first.ID)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel: %+v %v", cancelled, err)
	}
	if _, err = r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Three", Goal: "three"}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.service.GetTask(t.Context(), "other", parent, second.ID); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("cross-agent task: %v", err)
	}
}

func TestUserTwentyFirstTaskIsRejectedAfterTwentyQueuedSlots(t *testing.T) {
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	for i := range 20 {
		source.Key = fmt.Sprintf("bot/queue-%d", i)
		if _, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: fmt.Sprintf("Queue %d", i), Goal: "queued"}); err != nil {
			t.Fatalf("spawn %d: %v", i, err)
		}
	}
	source.Key = "bot/queue-20"
	if _, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Queue 20", Goal: "rejected"}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("21st spawn error: %v", err)
	}
	_ = parent
}

func TestUserReadOnlyTaskPromptDeclaresFileBoundary(t *testing.T) {
	prompt := taskInstructions("base", "inspect", sqlc.Task{ModifiesFiles: 0})
	if !strings.Contains(prompt, "do not create, edit, or delete files") || !strings.Contains(prompt, "Other read-only tasks may share this directory") {
		t.Fatalf("read-only prompt: %s", prompt)
	}
}
func TestUserTaskValidatesSourceAndRejectsBackgroundDelegation(t *testing.T) {
	r := startNative(t, "echo")
	_, source := taskParent(t, r)
	forged := source
	forged.SourceRequestID = "missing"
	if _, err := r.service.SpawnTask(t.Context(), forged, engine.SpawnTaskArgs{Title: "No", Goal: "no"}); err == nil {
		t.Fatal("unknown source accepted")
	}
	task, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Task", Goal: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := r.q.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := r.service.execution(t.Context(), row.ConversationID, "bot", engine.Options{Model: engine.ModelRef{Provider: "faux", ModelID: "echo"}})
	if err != nil {
		t.Fatal(err)
	}
	hidden := engine.TaskContext{AgentID: "bot", ConversationID: ref.ID, SourceRequestID: task.ID, Key: "bot/recursive"}
	if _, err = r.service.ToolListTasks(t.Context(), hidden); !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("background task list: %v", err)
	}
	if _, err = r.service.SpawnTask(t.Context(), hidden, engine.SpawnTaskArgs{Title: "Recursive", Goal: "no"}); !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("recursive spawn: %v", err)
	}
	if _, err = r.q.SettleSubmission(t.Context(), sqlc.SettleSubmissionParams{ID: "source", Status: "unanswered"}); err != nil {
		t.Fatal(err)
	}
	source.Key = "bot/late"
	if _, err = r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Late", Goal: "no"}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("inactive source: %v", err)
	}
}

func TestUserTaskReportAndResultReceiptCommitTogether(t *testing.T) {
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	task, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Atomic", Goal: "work"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.conn.ExecContext(t.Context(), `CREATE TRIGGER fail_task_result BEFORE INSERT ON submissions WHEN NEW.kind = 'task_result' BEGIN SELECT RAISE(FAIL, 'result receipt write failed'); END`)
	if err != nil {
		t.Fatal(err)
	}
	outcome := engine.Outcome{Status: "done", Answer: "full report"}
	if _, err = r.service.settleTask(t.Context(), task.ID, outcome); err == nil {
		t.Fatal("result receipt failure was hidden")
	}
	saved, err := r.service.GetTask(t.Context(), "bot", parent, task.ID)
	if err != nil || saved.Status != "queued" || saved.Report != "" {
		t.Fatalf("partial commit: %+v %v", saved, err)
	}
	if _, err = r.conn.ExecContext(t.Context(), "DROP TRIGGER fail_task_result"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.service.settleTask(t.Context(), task.ID, outcome); err != nil {
		t.Fatal(err)
	}
	result := receipt(t, r, "task:"+task.ID+":result")
	if result.Kind != "task_result" || result.Origin != "web" || result.ConversationID != parent {
		t.Fatalf("result metadata: %+v", result)
	}
	if _, err = r.service.settleTask(t.Context(), task.ID, outcome); err != nil {
		t.Fatal(err)
	}
	if err = r.service.Stop(t.Context(), "bot", parent); err != nil {
		t.Fatal(err)
	}
	if _, err = r.service.settleTask(t.Context(), task.ID, outcome); err != nil {
		t.Fatal(err)
	}
	if sub := receipt(t, r, result.ID); sub.Status != "unanswered" || sub.Reason.String != "stopped" {
		t.Fatalf("late result restored stopped receipt: %+v", sub)
	}
	saved, err = r.service.GetTask(t.Context(), "bot", parent, task.ID)
	if err != nil || saved.Report != "full report" {
		t.Fatalf("report lost: %+v %v", saved, err)
	}
}

func TestUserStopPreventsLateTaskResultFromWakingParent(t *testing.T) {
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	task, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Late", Goal: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.service.Stop(t.Context(), "bot", parent); err != nil {
		t.Fatal(err)
	}
	if _, err = r.service.settleTask(t.Context(), task.ID, engine.Outcome{Status: "done", Answer: "late report"}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.q.GetSubmission(t.Context(), "task:"+task.ID+":result"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cancelled task created late result: %v", err)
	}
	if sub := receipt(t, r, task.ID); sub.Reason.String != "stopped" {
		t.Fatalf("late task changed receipt: %+v", sub)
	}
}

func TestUserTaskResultEntersNativeInboxWhileParentIsBusy(t *testing.T) {
	r := startNative(t, "bash")
	marker := filepath.Join(r.dir, "started")
	release := filepath.Join(r.dir, "release")
	parent, err := r.service.Post(t.Context(), "bot", MainKey, "", "source", "touch "+marker+"; while [ ! -f "+release+" ]; do sleep 0.01; done")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(marker); return err == nil })
	c, err := r.service.Get(t.Context(), "bot", parent)
	if err != nil {
		t.Fatal(err)
	}
	task, err := r.service.SpawnTask(t.Context(), engine.TaskContext{AgentID: "bot", ConversationID: c.ExecutionID, SourceRequestID: "source", Key: "bot/result"}, engine.SpawnTaskArgs{Title: "Result", Goal: "work", Provider: "faux", ModelID: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.service.settleTask(t.Context(), task.ID, engine.Outcome{Status: "done", Answer: "report"}); err != nil {
		t.Fatal(err)
	}
	id := "task:" + task.ID + ":result"
	err = r.service.admit(t.Context(), taskInput(t, r, id))
	if err != nil {
		t.Fatal(err)
	}
	if !receipt(t, r, id).AdmittedAt.Valid {
		t.Fatal("busy parent rejected task result")
	}
	if err = r.service.Stop(t.Context(), "bot", parent); err != nil {
		t.Fatal(err)
	}
	if sub := receipt(t, r, id); sub.Reason.String != "stopped" {
		t.Fatalf("result not withdrawn: %+v", sub)
	}
	if err = os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestUserTaskRestartInterruptsAdmittedWorkBeforeResume(t *testing.T) {
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	task, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Interrupted", Goal: "work"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := r.q.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.service.execution(t.Context(), row.ConversationID, "bot", engine.Options{Model: engine.ModelRef{Provider: "faux", ModelID: "echo"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.q.StartTask(t.Context(), sqlc.StartTaskParams{ID: task.ID, Now: sql.NullInt64{Int64: 1, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err = r.agents.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	next, err := engine.New(engine.Config{DataDir: r.dir, Home: t.TempDir(), FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close(context.Background())
	service := New(r.conn, next, time.Now, nil, nil)
	defer service.cancel()
	if err = service.RecoverTasks(t.Context()); err != nil {
		t.Fatal(err)
	}
	saved, err := service.GetTask(t.Context(), "bot", parent, task.ID)
	if err != nil || saved.Status != "interrupted" {
		t.Fatalf("recovery: %+v %v", saved, err)
	}
	if sub := receipt(t, r, task.ID); sub.Reason.String != "interrupted" {
		t.Fatalf("task receipt: %+v", sub)
	}
	// A second restart before native open must retain the first restart's isolation.
	if err = next.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	again, err := engine.New(engine.Config{DataDir: r.dir, Home: t.TempDir(), FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close(context.Background())
	restarted := New(r.conn, again, time.Now, nil, nil)
	defer func() {
		restarted.cancel()
		restarted.wg.Wait()
	}()
	if err = restarted.RecoverTasks(t.Context()); err != nil {
		t.Fatal(err)
	}
	conversation, err := r.q.GetConversation(t.Context(), row.ConversationID)
	if err != nil || conversation.AbortPending != 1 {
		t.Fatalf("repeated restart lost isolation: %+v %v", conversation, err)
	}
	// Native cancellation consumes the durable fact only after an actual open.
	err = restarted.reconcileAgent(t.Context(), "bot")
	if err != nil {
		t.Fatal(err)
	}
	conversation, err = r.q.GetConversation(t.Context(), row.ConversationID)
	if err != nil || conversation.AbortPending != 0 {
		t.Fatalf("native cancellation did not acknowledge isolation: %+v %v", conversation, err)
	}
}

func TestUserSavedCancellationIsReconciledBeforeLaterAdmission(t *testing.T) {
	r := startNative(t, "echo")
	id, err := r.service.Post(t.Context(), "bot", MainKey, "", "first", "first")
	if err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "first")
	// Keep the cancellation fact durable when the final acknowledgement write fails.
	_, err = r.conn.ExecContext(t.Context(), `CREATE TRIGGER fail_abort_ack BEFORE UPDATE OF abort_pending ON conversations WHEN NEW.abort_pending = 0 BEGIN SELECT RAISE(FAIL, 'abort acknowledgement failed'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.service.Stop(t.Context(), "bot", id); err == nil {
		t.Fatal("cancellation acknowledgement failure was hidden")
	}
	c, err := r.q.GetConversation(t.Context(), id)
	if err != nil || c.AbortPending != 1 {
		t.Fatalf("cancellation fact: %+v %v", c, err)
	}
	if _, err = r.service.Post(t.Context(), "bot", MainKey, "", "later", "later"); err != nil {
		t.Fatalf("durable input rejected while cancellation retries: %v", err)
	}
	if receipt(t, r, "later").AdmittedAt.Valid {
		t.Fatal("later input admitted before cancellation acknowledgement")
	}
	if _, err = r.conn.ExecContext(t.Context(), "DROP TRIGGER fail_abort_ack"); err != nil {
		t.Fatal(err)
	}
	runReconciler(t, r)
	if sub := awaitReceipt(t, r, "later"); sub.Answer.String != "echo: later" {
		t.Fatalf("later answer: %+v", sub)
	}
	c, err = r.q.GetConversation(t.Context(), id)
	if err != nil || c.AbortPending != 0 {
		t.Fatalf("cancellation still pending: %+v %v", c, err)
	}
}

func TestUserNativeTaskToolSavesSourceAndDeliversReport(t *testing.T) {
	r := startNative(t, "spawn")
	runReconciler(t, r)
	parent, err := r.service.Post(t.Context(), "bot", MainKey, "", "native-source", `{"title":"Child","goal":"bounded work","provider":"faux","modelId":"echo"}`)
	if err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "native-source")
	var task sqlc.Task
	waitFor(t, func() bool {
		tasks, err := r.q.ListTasks(t.Context(), parent)
		if err != nil {
			t.Fatal(err)
		}
		if len(tasks) != 1 {
			return false
		}
		task = tasks[0]
		return task.Status == "done"
	})
	if task.SourceRequestID != "native-source" || task.Report != "echo: bounded work" {
		t.Fatalf("native task: %+v", task)
	}
	result := awaitReceipt(t, r, "task:"+task.ID+":result")
	if result.Status != "done" || !result.AdmittedAt.Valid {
		t.Fatalf("native task follow-up: %+v", result)
	}
}

func TestUserCancellingCompletedTaskLeavesItsResultAvailable(t *testing.T) {
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	task, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Completed", Goal: "saved report"})
	if err != nil {
		t.Fatal(err)
	}
	// Given a real completed task and its saved, undelivered parent result.
	err = r.service.admit(t.Context(), taskInput(t, r, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, task.ID)
	before, err := r.q.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	resultID := "task:" + task.ID + ":result"
	resultBefore := receipt(t, r, resultID)
	if before.Status != "done" || before.Report != "echo: saved report" || resultBefore.Status != "pending" {
		t.Fatalf("completed task: %+v result: %+v", before, resultBefore)
	}
	// When the user cancels the completed task, its report and delivery stay intact.
	if _, err = r.service.CancelTask(t.Context(), "bot", parent, task.ID); err != nil {
		t.Fatal(err)
	}
	after, err := r.q.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("completed task changed: before=%+v after=%+v", before, after)
	}
	if resultAfter := receipt(t, r, resultID); resultAfter != resultBefore {
		t.Fatalf("saved result changed: before=%+v after=%+v", resultBefore, resultAfter)
	}
}
