package task

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserSeesOneSourceChangeAndItsWaitAfterRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	s, conn, woken := openEventStore(t, path)
	if _, err := s.q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: "default", Name: "Dex", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask(ctx, Owner, OriginUser, CreateTaskParams{AssigneeBotID: "default", Title: "Review the change"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateTask(ctx, Owner, OriginUser, CreateTaskParams{AssigneeBotID: "default", Title: "Other work"})
	if err != nil {
		t.Fatal(err)
	}
	object := Object{Type: "proposal", Key: "1"}
	unrelated := Object{Type: "proposal", Key: "2"}
	const namespace = "test/account"
	if err := s.LinkObject(ctx, "default", task.ID, namespace, object); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkObject(ctx, "default", other.ID, namespace, unrelated); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkObject(ctx, "default", other.ID, namespace, object); !errors.Is(err, ErrObjectConflict) {
		t.Fatalf("expected object conflict, got %v", err)
	}
	if err := s.AddWait(ctx, "default", task.ID, Wait{Namespace: namespace, Type: "review", Object: object, Revision: "v2", AfterAt: 100, Alert: true}); err != nil {
		t.Fatal(err)
	}

	old := Event{Namespace: namespace, ReceiptKey: "push-old", ChangeKey: "review-1", Type: "review", Objects: []Object{object}, Revision: "v2", Content: "Old review", OccurredAt: 100}
	if result, err := s.Accept(ctx, old); err != nil || result.ResolvedWaits != 0 || result.TaskID != task.ID {
		t.Fatalf("old event: %+v, %v", result, err)
	}
	conflict := Event{Namespace: namespace, ReceiptKey: "push-conflict", Type: "review", Objects: []Object{object, unrelated}, Revision: "v2", Content: "Ambiguous review", OccurredAt: 102}
	if result, err := s.Accept(ctx, conflict); err != nil || result.Disposition != "conflict" || result.ResolvedWaits != 0 {
		t.Fatalf("conflicting event: %+v, %v", result, err)
	}
	match := old
	match.ReceiptKey, match.ChangeKey, match.OccurredAt, match.Content = "push-new", "review-2", 103, "New review"
	accepted, err := s.Accept(ctx, match)
	if err != nil || accepted.ResolvedWaits != 1 || accepted.Duplicate || !accepted.Alerted || len(*woken) != 1 {
		t.Fatalf("matching event: %+v, %v, woken %v", accepted, err, *woken)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	s, _, woken = openEventStore(t, path)
	for _, key := range []string{"push-new", "pull-new", "pull-new"} {
		match.ReceiptKey = key
		replay, err := s.Accept(ctx, match)
		if err != nil || replay.EventID != accepted.EventID || !replay.Duplicate || replay.ResolvedWaits != 0 || replay.Alerted {
			t.Fatalf("replay %s: %+v, %v", key, replay, err)
		}
	}
	if len(*woken) != 0 {
		t.Fatalf("a replay woke Main: %v", *woken)
	}
	var alertID, content string
	err = s.db.QueryRowContext(ctx, `SELECT id, content FROM submissions WHERE kind = 'task_wake' AND status = 'pending'`).Scan(&alertID, &content)
	if err != nil || strings.Contains(content, "New review") || !strings.Contains(content, `"reason":"gate_unblocked"`) {
		t.Fatalf("Main must have a pointer-only wake: %q, %v", content, err)
	}
	if linked, err := s.q.TaskForInput(ctx, alertID); err != nil || linked != task.ID {
		t.Fatalf("the wake must belong to the Task: %q, %v", linked, err)
	}
	var alerts int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM submissions").Scan(&alerts); err != nil || alerts != 1 {
		t.Fatalf("expected exactly one wake, got %d, %v", alerts, err)
	}
	detail, err := s.Detail(ctx, task.ID)
	if err != nil || len(detail.Entries) != 5 || detail.Entries[4].Content != "New review" {
		t.Fatalf("timeline: %+v, %v", detail, err)
	}
	waits := detail.Waits
	if len(waits) != 1 || waits[0].ResolvedBy.String != accepted.EventID {
		t.Fatalf("waits after restart: %+v", waits)
	}
}

func TestUserGetsNoAlertWithoutAnAlertRule(t *testing.T) {
	ctx := t.Context()
	s, conn, woken := openEventStore(t, filepath.Join(t.TempDir(), "control.sqlite"))
	if _, err := s.q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: "default", Name: "Dex", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	const namespace = "test/account"
	quiet, loud, closed := Object{Type: "proposal", Key: "quiet"}, Object{Type: "proposal", Key: "loud"}, Object{Type: "proposal", Key: "closed"}
	waits := map[Object]bool{quiet: false, loud: true, closed: true}
	tasks := map[Object]string{}
	for object, alert := range waits {
		task, err := s.CreateTask(ctx, Owner, OriginUser, CreateTaskParams{AssigneeBotID: "default", Title: object.Key})
		if err != nil {
			t.Fatal(err)
		}
		tasks[object] = task.ID
		if err := s.BindObject(ctx, "default", task.ID, namespace, object, &Wait{Namespace: namespace, Type: "review", Object: object, AfterAt: 1, Alert: alert}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.UpdateTask(ctx, tasks[closed], UpdateParams{Status: new(StatusCancelled)}); err != nil {
		t.Fatal(err)
	}
	review := func(key string, object Object, recordOnly bool) Receipt {
		t.Helper()
		got, err := s.Accept(ctx, Event{Namespace: namespace, ReceiptKey: key, ChangeKey: key, Type: "review", Objects: []Object{object}, Content: "Approve", OccurredAt: 10, RecordOnly: recordOnly})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// A wait without an alert resolves silently.
	if got := review("quiet", quiet, false); got.ResolvedWaits != 1 || got.Alerted {
		t.Fatalf("a wait without an alert alerted Main: %+v", got)
	}
	// A record-only event and an event on a closed Task resolve nothing and alert nobody.
	if got := review("outsider", loud, true); got.ResolvedWaits != 0 || got.Alerted {
		t.Fatalf("a record-only event alerted Main: %+v", got)
	}
	if got := review("late", closed, false); got.ResolvedWaits != 0 || got.Alerted {
		t.Fatalf("an event on a closed Task alerted Main: %+v", got)
	}
	// An unmatched event has no Task and so no alert.
	if got := review("stray", Object{Type: "proposal", Key: "unknown"}, false); got.Disposition != "unmatched" || got.Alerted {
		t.Fatalf("an unmatched event alerted Main: %+v", got)
	}
	if len(*woken) != 0 {
		t.Fatalf("Main was woken: %v", *woken)
	}
	var alerts int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM submissions").Scan(&alerts); err != nil || alerts != 0 {
		t.Fatalf("expected no alerts, got %d, %v", alerts, err)
	}
	// The alert wait still alerts once a trusted review arrives.
	if got := review("member", loud, false); got.ResolvedWaits != 1 || !got.Alerted || len(*woken) != 1 {
		t.Fatalf("the alert wait did not alert Main: %+v, %v", got, *woken)
	}
}

func openEventStore(t *testing.T, path string) (*Service, *sql.DB, *[]int64) {
	t.Helper()
	conn, err := db.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	woken := new([]int64)
	return New(conn, func(id int64) { *woken = append(*woken, id) }), conn, woken
}
