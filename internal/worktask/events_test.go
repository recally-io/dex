package worktask

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserSeesOneSourceChangeAndItsWaitAfterRestart(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	s, conn := openEventStore(t, path)
	if _, err := s.q.CreateAgent(ctx, sqlc.CreateAgentParams{ID: "default", Name: "Dex", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	task, err := s.Create(ctx, "default", "Review the change")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Create(ctx, "default", "Other work")
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
	if err := s.AddWait(ctx, "default", task.ID, Wait{Namespace: namespace, Type: "review", Object: object, Revision: "v2", AfterAt: 100}); err != nil {
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
	if err != nil || accepted.ResolvedWaits != 1 || accepted.Duplicate {
		t.Fatalf("matching event: %+v, %v", accepted, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	s, _ = openEventStore(t, path)
	for _, key := range []string{"push-new", "pull-new", "pull-new"} {
		match.ReceiptKey = key
		replay, err := s.Accept(ctx, match)
		if err != nil || replay.EventID != accepted.EventID || !replay.Duplicate || replay.ResolvedWaits != 0 {
			t.Fatalf("replay %s: %+v, %v", key, replay, err)
		}
	}
	detail, err := s.Detail(ctx, "default", task.ID)
	if err != nil || len(detail.Entries) != 2 || detail.Entries[0].Content != "New review" {
		t.Fatalf("timeline: %+v, %v", detail, err)
	}
	waits, err := s.ListWaits(ctx, "default", task.ID)
	if err != nil || len(waits) != 1 || waits[0].ResolvedBy.String != accepted.EventID {
		t.Fatalf("waits after restart: %+v, %v", waits, err)
	}
}

func openEventStore(t *testing.T, path string) (*Service, *sql.DB) {
	t.Helper()
	conn, err := db.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return New(conn), conn
}
