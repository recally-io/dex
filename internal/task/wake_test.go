package task_test

import (
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	tasksvc "github.com/recally-io/dex/internal/task"
)

func TestUserGetsOnePendingWakeForFiveConcurrentSourceEvents(t *testing.T) {
	conn, q, svc := openBoardTest(t)
	ctx := t.Context()

	// Given Main has no admission worker, when five source events create Tasks at once.
	var wg sync.WaitGroup
	for i := range 5 {
		wg.Go(func() { createSourceWake(t, svc, fmt.Sprint(i)) })
	}
	wg.Wait()

	// Then one wake has a pointer to every Task and no external text or Task association.
	wake := pendingWake(t, q, 1)
	payload := readWake(t, wake)
	var highWater, count int64
	if err := conn.QueryRowContext(ctx, `SELECT max(id), count(*) FROM task_entries`).Scan(&highWater, &count); err != nil {
		t.Fatal(err)
	}
	if payload.HighWaterEntryID != highWater || count != 5 {
		t.Fatalf("wake cursor = %d; latest entry = %d, entry count = %d", payload.HighWaterEntryID, highWater, count)
	}
	keys := make(map[string]string)
	for _, item := range payload.Items {
		keys[item.TaskKey] = item.Reason
	}
	want := map[string]string{"T-1": tasksvc.WakeAssigned, "T-2": tasksvc.WakeAssigned, "T-3": tasksvc.WakeAssigned, "T-4": tasksvc.WakeAssigned, "T-5": tasksvc.WakeAssigned}
	if len(payload.Items) != 5 || !maps.Equal(keys, want) {
		t.Fatalf("wake items = %+v; want %+v", payload.Items, want)
	}
	if task, err := q.TaskForInput(ctx, wake.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("a wake for multiple Tasks must have no Task association: %q, %v", task, err)
	}
	assertWakeCount(t, conn, 1)
}

func TestUserKeepsLatestWakeWhenAdmissionOverlapsWithAnAlert(t *testing.T) {
	conn, q, svc := openBoardTest(t)
	ctx := t.Context()

	// Given a single Task has a wake, and two later events resolve alert waits while admission is in flight.
	taskID := createSourceWake(t, svc, "one")
	first := pendingWake(t, q, 1)
	object := tasksvc.Object{Type: "issue", Key: "one"}
	for i := range 2 {
		after := int64(i + 2)
		wait := tasksvc.Wait{Namespace: "source", Type: "reviewed", Object: object, AfterAt: after, Alert: true}
		if err := svc.AddWait(ctx, "assignee", taskID, wait); err != nil {
			t.Fatal(err)
		}
		r, err := svc.Accept(ctx, tasksvc.Event{Namespace: "source", ReceiptKey: fmt.Sprint(after), Type: wait.Type, Objects: []tasksvc.Object{object}, OccurredAt: after + 1})
		if err != nil || !r.Alerted || r.ResolvedWaits != 1 {
			t.Fatalf("alert wait: %+v, %v", r, err)
		}
	}
	latest := pendingWake(t, q, 1)
	want := []tasksvc.WakeItem{{TaskKey: "T-1", Reason: tasksvc.WakeAssigned}, {TaskKey: "T-1", Reason: tasksvc.WakeGateUnblocked}}
	if first.ID != latest.ID || !slices.Equal(readWake(t, latest).Items, want) || readWake(t, latest).HighWaterEntryID <= readWake(t, first).HighWaterEntryID {
		t.Fatalf("later alerts must update the cursor without adding a duplicate pointer: %s -> %s", first.Content, latest.Content)
	}

	// When the original admission commits, a new pending receipt carries the complete latest wake.
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	txq := q.WithTx(tx)
	if err := txq.AdmitSubmission(ctx, sqlc.AdmitSubmissionParams{ID: first.ID, AdmittedAt: sql.NullInt64{Int64: 10, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := tasksvc.RequeueTaskWake(ctx, txq, latest, 10); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	requeued := pendingWake(t, q, 1)
	if requeued.ID == first.ID || requeued.Content != latest.Content {
		t.Fatalf("rollover wake = %+v; want new ID and %s", requeued, latest.Content)
	}
	if linked, err := q.TaskForInput(ctx, requeued.ID); err != nil || linked != taskID {
		t.Fatalf("rollover lost its single Task association: %q, %v", linked, err)
	}
	var lastEntry int64
	if err := conn.QueryRowContext(ctx, `SELECT max(id) FROM task_entries`).Scan(&lastEntry); err != nil || lastEntry != readWake(t, requeued).HighWaterEntryID {
		t.Fatalf("rollover must not append Task entries: latest = %d, %v", lastEntry, err)
	}

	// An event after admission gets a new slot; a stopped unadmitted wake does not block it.
	boardExec(t, conn, `UPDATE submissions SET status = 'unanswered' WHERE id = ?`, requeued.ID)
	createSourceWake(t, svc, "two")
	next := pendingWake(t, q, 1)
	if next.ID == requeued.ID || !slices.Equal(readWake(t, next).Items, []tasksvc.WakeItem{{TaskKey: "T-2", Reason: tasksvc.WakeAssigned}}) {
		t.Fatalf("new event must occupy a fresh slot: %+v", next)
	}
	assertWakeCount(t, conn, 3)
}

func TestUserGetsCrossBotReviewWakesOnlyAfterCommit(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	conn, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	boardExec(t, conn, `INSERT INTO agents (id, name, model_provider, model_id, created_at) VALUES
		('assignee', 'Assignee', 'faux', 'echo', 1), ('reviewer', 'Reviewer', 'faux', 'echo', 1)`)
	q := sqlc.New(conn)
	var woken []string
	svc := tasksvc.New(conn, func(id int64) {
		conv, err := q.GetConversation(t.Context(), id)
		if err != nil || conv.ChannelKey != "main" {
			t.Fatalf("wake did not target a committed Main: %+v, %v", conv, err)
		}
		woken = append(woken, conv.AgentID)
		wake := pendingWake(t, q, id)
		payload := readWake(t, wake)
		var kind string
		if err := conn.QueryRowContext(t.Context(), `SELECT kind FROM task_entries WHERE id = ?`, payload.HighWaterEntryID).Scan(&kind); err != nil {
			t.Fatalf("wake cursor has no committed entry: %v", err)
		}
		wantKind := tasksvc.KindReview
		if conv.AgentID == "reviewer" {
			wantKind = tasksvc.KindStatus
		}
		if kind != wantKind || len(payload.Items) != 1 {
			t.Fatalf("unexpected wake for %s: %s, %+v", conv.AgentID, kind, payload)
		}
	})

	// Given a cross-Bot reviewer and a Task with an ordinary note and artifact.
	task := createBoardTestTask(t, svc, "Review work", tasksvc.StatusInProgress, "reviewer")
	if _, err := svc.PostEntry(ctx, tasksvc.Owner, tasksvc.OriginUser, task.ID, tasksvc.PostEntryParams{Kind: tasksvc.KindArtifact, Content: "Build output"}, nil); err != nil {
		t.Fatal(err)
	}
	assertWakeCount(t, conn, 0)

	// When the Assignee asks for a review and the Reviewer asks for changes.
	submitted, err := svc.TransitionStatus(ctx, tasksvc.Bot("assignee"), tasksvc.OriginUser, task.ID, tasksvc.StatusInReview, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Review(ctx, tasksvc.Bot("reviewer"), tasksvc.OriginTaskWake, task.ID, tasksvc.ReviewRequestChanges, submitted.ReviewSeq, "Private review feedback"); err != nil {
		t.Fatal(err)
	}

	// Then after reopening the database, each recipient still has one wake.
	if !slices.Equal(woken, []string{"reviewer", "assignee"}) {
		t.Fatalf("woken Bots = %v", woken)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	q = sqlc.New(reopened)
	for bot, reason := range map[string]string{"assignee": tasksvc.WakeChangesRequested, "reviewer": tasksvc.WakeReviewRequested} {
		main, err := q.GetTaskMain(ctx, bot)
		if err != nil {
			t.Fatal(err)
		}
		wake := pendingWake(t, q, main)
		if items := readWake(t, wake).Items; !slices.Equal(items, []tasksvc.WakeItem{{TaskKey: "T-1", Reason: reason}}) {
			t.Fatalf("wake for %s = %s", bot, wake.Content)
		}
		linked, err := q.TaskForInput(ctx, wake.ID)
		if bot == "reviewer" && !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("reviewer wake must not link replies or children to the Task: %q, %v", linked, err)
		}
		if bot == "assignee" && (err != nil || linked != task.ID) {
			t.Fatalf("assignee wake must retain its Task link: %q, %v", linked, err)
		}
	}
	restartedService := tasksvc.New(reopened, nil)
	if _, err := restartedService.Review(ctx, tasksvc.Bot("reviewer"), tasksvc.OriginTaskWake, task.ID, tasksvc.ReviewRequestChanges, submitted.ReviewSeq, "Replay"); !errors.Is(err, tasksvc.ErrConflict) {
		t.Fatalf("stale review = %v; want conflict", err)
	}
	assertWakeCount(t, reopened, 2)
}

func createSourceWake(t *testing.T, svc *tasksvc.Service, key string) string {
	t.Helper()
	r, err := svc.AcceptCreating(t.Context(), tasksvc.Event{
		Namespace: "source", ReceiptKey: key, Type: "opened", Objects: []tasksvc.Object{{Type: "issue", Key: key}},
		Content: "Untrusted entry body", SourceURL: "https://example.invalid/source", OccurredAt: 1,
	}, "assignee", "Source task title")
	if err != nil || !r.Alerted || r.TaskID == "" {
		t.Errorf("source event %s: %+v, %v", key, r, err)
	}
	return r.TaskID
}

func pendingWake(t *testing.T, q *sqlc.Queries, main int64) sqlc.Submission {
	t.Helper()
	wake, err := q.GetUnadmittedTaskWake(t.Context(), main)
	if err != nil {
		t.Fatalf("pending wake for Main %d: %v", main, err)
	}
	return wake
}

func readWake(t *testing.T, wake sqlc.Submission) tasksvc.WakePayload {
	t.Helper()
	var payload tasksvc.WakePayload
	if err := json.Unmarshal([]byte(wake.Content), &payload, json.RejectUnknownMembers(true)); err != nil || payload.HighWaterEntryID <= 0 {
		t.Fatalf("expected a pointer-only wake, got %q, %v", wake.Content, err)
	}
	return payload
}

func assertWakeCount(t *testing.T, conn *sql.DB, want int) {
	t.Helper()
	var got int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM submissions WHERE kind = 'task_wake'`).Scan(&got); err != nil || got != want {
		t.Fatalf("wake count = %d, %v; want %d", got, err, want)
	}
}
