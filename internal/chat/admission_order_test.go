package chat

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

type nativeTest struct {
	service *Service
	agents  *engine.Agents
	conn    *sql.DB
	q       *sqlc.Queries
	dir     string
}

func startNative(t *testing.T, model string) *nativeTest {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Open(t.Context(), filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(conn)
	if err = q.CreateAgentIfMissing(t.Context(), sqlc.CreateAgentIfMissingParams{ID: "bot", Name: "Bot"}); err != nil {
		t.Fatal(err)
	}
	if err = q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{ID: "bot", Name: "Bot", ModelProvider: "faux", ModelID: model, ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	var service *Service
	agents, err := engine.New(engine.Config{DataDir: dir, Home: t.TempDir(), BaseEnv: map[string]string{"PATH": os.Getenv("PATH")}, FakeModel: true, Tasks: engine.TaskTools{
		Spawn: func(ctx context.Context, c engine.TaskContext, a engine.SpawnTaskArgs) (engine.TaskInfo, error) {
			t, err := service.SpawnTask(ctx, c, a)
			return engine.TaskInfo(t), err
		},
		List: func(ctx context.Context, c engine.TaskContext) ([]engine.TaskInfo, error) {
			tasks, err := service.ToolListTasks(ctx, c)
			out := make([]engine.TaskInfo, len(tasks))
			for i, t := range tasks {
				out[i] = engine.TaskInfo(t)
			}
			return out, err
		},
		Get: func(ctx context.Context, c engine.TaskContext, id string) (engine.TaskInfo, error) {
			t, err := service.ToolGetTask(ctx, c, id)
			return engine.TaskInfo(t), err
		},
		Cancel: func(ctx context.Context, c engine.TaskContext, id string) (engine.TaskInfo, error) {
			t, err := service.ToolCancelTask(ctx, c, id)
			return engine.TaskInfo(t), err
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	service = New(conn, agents, time.Now, nil, nil)
	t.Cleanup(func() {
		service.cancel()
		service.wg.Wait()
		if err := agents.Close(context.Background()); err != nil {
			t.Error(err)
		}
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	})
	return &nativeTest{service, agents, conn, q, dir}
}

func waitFor(t *testing.T, check func() bool) {
	t.Helper()
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if check() {
			return
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-timer.C:
			t.Fatal("condition did not become true")
		case <-tick.C:
		}
	}
}
func receipt(t *testing.T, r *nativeTest, id string) sqlc.Submission {
	t.Helper()
	sub, err := r.q.GetSubmission(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return sub
}
func awaitReceipt(t *testing.T, r *nativeTest, id string) sqlc.Submission {
	t.Helper()
	waitFor(t, func() bool { return receipt(t, r, id).Status != "pending" })
	return receipt(t, r, id)
}
func runReconciler(t *testing.T, r *nativeTest) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); r.service.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestUserInputIsDurablyAdmittedBeforeSendReturns(t *testing.T) {
	// Given native execution and a real receipt database.
	r := startNative(t, "echo")
	// When the caller sends a message, native admission precedes the return.
	_, err := r.service.Post(t.Context(), "bot", MainKey, "", "first", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !receipt(t, r, "first").AdmittedAt.Valid {
		t.Fatal("send returned without durable admission")
	}
	// Then asynchronous settlement projects the answer.
	sub := awaitReceipt(t, r, "first")
	if sub.Status != "done" || sub.Answer.String != "echo: hello" {
		t.Fatalf("receipt: %+v", sub)
	}
}

func TestUserKeepsInputAdmittedAfterClear(t *testing.T) {
	r := startNative(t, "context")
	id, err := r.service.Post(t.Context(), "bot", MainKey, "", "before", "old input")
	if err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "before")
	if err = r.service.Clear(t.Context(), "bot", id, "reset", "web"); err != nil {
		t.Fatal(err)
	}
	if err = r.service.Clear(t.Context(), "bot", id, "reset", "web"); err != nil {
		t.Fatal(err)
	}
	// The clock can move backwards. Admission order still defines the context.
	r.service.now = func() time.Time { return time.UnixMilli(1) }
	if _, err = r.service.Post(t.Context(), "bot", MainKey, "", "after", "new input"); err != nil {
		t.Fatal(err)
	}
	sub := awaitReceipt(t, r, "after")
	if !strings.Contains(sub.Answer.String, "new input") || strings.Contains(sub.Answer.String, "old input") {
		t.Fatalf("context after clear: %s", sub.Answer.String)
	}
	if err = r.service.Clear(t.Context(), "bot", id, "after", "web"); !errors.Is(err, ErrResetID) {
		t.Fatalf("reused message ID: %v", err)
	}
}

func TestUserRetryCompletesReceiptWithoutAdmission(t *testing.T) {
	r := startNative(t, "echo")
	if err := r.service.AcquireMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Maintenance accepts the channel intent without claiming native admission.
	err := r.service.SubmitMessage(t.Context(), "bot", "telegram:7", "source-7", Message{Content: "retry me"})
	if err != nil {
		t.Fatalf("durable channel intent rejected: %v", err)
	}
	id, err := r.q.GetSubmissionBySource(t.Context(), sql.NullString{String: "source-7", Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if receipt(t, r, id).AdmittedAt.Valid {
		t.Fatal("paused receipt claims admission")
	}
	if err = r.service.ReleaseMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = r.service.SubmitMessage(t.Context(), "bot", "telegram:7", "source-7", Message{Content: "must not replace content"}); err != nil {
		t.Fatal(err)
	}
	if !receipt(t, r, id).AdmittedAt.Valid {
		t.Fatal("duplicate source skipped native admission")
	}
	sub := awaitReceipt(t, r, id)
	if sub.Answer.String != "echo: retry me" || !sub.AdmittedAt.Valid {
		t.Fatalf("retry: %+v", sub)
	}
	var count int
	if err = r.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE source_key = 'source-7'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("receipts=%d err=%v", count, err)
	}
}

func TestUserReceiptReconcilesAfterAdmissionMarkerWriteFails(t *testing.T) {
	r := startNative(t, "bash")
	// Fail the control write after native admission, using a real SQLite trigger.
	_, err := r.conn.ExecContext(t.Context(), `CREATE TRIGGER fail_admission BEFORE UPDATE OF admitted_at ON submissions BEGIN SELECT RAISE(FAIL, 'admission write failed'); END`)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(r.dir, "once")
	_, err = r.service.Post(t.Context(), "bot", MainKey, "", "once", "printf x >> "+output)
	if err != nil {
		t.Fatalf("durable intent rejected after marker failure: %v", err)
	}
	if receipt(t, r, "once").AdmittedAt.Valid {
		t.Fatal("marker persisted despite trigger")
	}
	if _, err = r.conn.ExecContext(t.Context(), "DROP TRIGGER fail_admission"); err != nil {
		t.Fatal(err)
	}
	runReconciler(t, r)
	sub := awaitReceipt(t, r, "once")
	if sub.Status != "done" {
		t.Fatalf("receipt: %+v", sub)
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "x" {
		t.Fatalf("execution repeated: %q, %v", data, err)
	}
}

func TestUserSettlementWriteFailureRetriesWithoutAnotherInput(t *testing.T) {
	r := startNative(t, "echo")
	_, err := r.conn.ExecContext(t.Context(), `CREATE TRIGGER fail_settlement BEFORE UPDATE OF status ON submissions BEGIN SELECT RAISE(FAIL, 'settlement write failed'); END`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.service.Post(t.Context(), "bot", MainKey, "", "settle", "saved answer")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, waiting := r.service.waiting.Load("settle"); return !waiting })
	if receipt(t, r, "settle").Status != "pending" {
		t.Fatal("settlement trigger did not hold receipt")
	}
	if _, err = r.conn.ExecContext(t.Context(), "DROP TRIGGER fail_settlement"); err != nil {
		t.Fatal(err)
	}
	runReconciler(t, r)
	if sub := awaitReceipt(t, r, "settle"); sub.Answer.String != "echo: saved answer" {
		t.Fatalf("reconciled answer: %+v", sub)
	}
}

func TestUserDuplicateDeterministicRejectionReturnsSavedFailure(t *testing.T) {
	r := startNative(t, "")
	err := r.service.SubmitMessage(t.Context(), "bot", "telegram:7", "rejected", Message{Content: "no configured model"})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("rejection: %v", err)
	}
	id, err := r.q.GetSubmissionBySource(t.Context(), sql.NullString{String: "rejected", Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	first := receipt(t, r, id)
	err = r.service.SubmitMessage(t.Context(), "bot", "telegram:7", "rejected", Message{Content: "retry"})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("saved rejection: %v", err)
	}
	second := receipt(t, r, id)
	if first.Content != second.Content || second.AdmittedAt.Valid || second.Status != "unanswered" {
		t.Fatalf("rejection retried execution: %+v", second)
	}
}

func TestUserCancellationFailureDoesNotBlockOtherConversations(t *testing.T) {
	r := startNative(t, "echo")
	// Given one conversation whose durable cancellation cannot be acknowledged.
	id, err := r.service.Post(t.Context(), "bot", MainKey, "", "first", "first")
	if err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "first")
	_, err = r.conn.ExecContext(t.Context(), `CREATE TRIGGER fail_abort_ack BEFORE UPDATE OF abort_pending ON conversations WHEN NEW.abort_pending = 0 BEGIN SELECT RAISE(FAIL, 'abort acknowledgement failed'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.service.Stop(t.Context(), "bot", id); err == nil {
		t.Fatal("expected cancellation acknowledgement failure")
	}
	// When users send to another conversation and another agent, both can run.
	if err = r.q.CreateAgentIfMissing(t.Context(), sqlc.CreateAgentIfMissingParams{ID: "other", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	if err = r.q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{ID: "other", Name: "Other", ModelProvider: "faux", ModelID: "echo", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"bot", "other"} {
		request := "healthy-" + agent
		if _, err = r.service.Post(t.Context(), agent, "chat:healthy", "", request, "healthy"); err != nil {
			t.Errorf("unrelated cancellation rejected %s: %v", agent, err)
		}
		if !receipt(t, r, request).AdmittedAt.Valid {
			t.Fatalf("unrelated cancellation blocked %s", agent)
		}
		if sub := awaitReceipt(t, r, request); sub.Answer.String != "echo: healthy" {
			t.Fatalf("healthy answer: %+v", sub)
		}
	}
}

func TestUserClearAcceptsDurableResetAfterAdmissionWriteFailure(t *testing.T) {
	r := startNative(t, "context")
	id, err := r.service.Post(t.Context(), "bot", MainKey, "", "before", "old input")
	if err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "before")
	// Given a native reset whose receipt acknowledgement temporarily fails.
	_, err = r.conn.ExecContext(t.Context(), `CREATE TRIGGER fail_reset_ack BEFORE UPDATE OF admitted_at ON submissions WHEN NEW.id = 'reset' BEGIN SELECT RAISE(FAIL, 'reset acknowledgement failed'); END`)
	if err != nil {
		t.Fatal(err)
	}
	// When Clear has saved the reset, it accepts responsibility for retrying.
	if err = r.service.Clear(t.Context(), "bot", id, "reset", "web"); err != nil {
		t.Fatalf("durable reset rejected: %v", err)
	}
	if receipt(t, r, "reset").AdmittedAt.Valid {
		t.Fatal("failed acknowledgement claims admission")
	}
	if _, err = r.conn.ExecContext(t.Context(), "DROP TRIGGER fail_reset_ack"); err != nil {
		t.Fatal(err)
	}
	runReconciler(t, r)
	awaitReceipt(t, r, "reset")
	// Then the saved reset completes without another request from the user.
	if _, err = r.service.Post(t.Context(), "bot", MainKey, "", "after", "new input"); err != nil {
		t.Fatal(err)
	}
	if sub := awaitReceipt(t, r, "after"); strings.Contains(sub.Answer.String, "old input") || !strings.Contains(sub.Answer.String, "new input") {
		t.Fatalf("context: %+v", sub)
	}
}

func TestUserStoppingMissingExecutionCompletesCancellation(t *testing.T) {
	r := startNative(t, "echo")
	c, err := r.service.Main(t.Context(), "bot")
	if err != nil {
		t.Fatal(err)
	}
	// Given an execution that no longer exists in native storage.
	if err = r.q.SetExecution(t.Context(), sqlc.SetExecutionParams{ID: c.ID, ExecutionID: sql.NullInt64{Int64: 999999, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	// When the user stops it, absence already satisfies cancellation.
	if err = r.service.Stop(t.Context(), "bot", c.ID); err != nil {
		t.Fatalf("missing execution cancellation: %v", err)
	}
	saved, err := r.q.GetConversation(t.Context(), c.ID)
	if err != nil || saved.AbortPending != 0 {
		t.Fatalf("cancellation remains pending: %+v %v", saved, err)
	}
}
