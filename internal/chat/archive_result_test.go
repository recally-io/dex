package chat

import (
	"errors"
	"testing"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserArchivedBotRejectsClear(t *testing.T) {
	r, chatID, _ := archiveFixture(t)
	if _, err := r.conn.ExecContext(t.Context(), "UPDATE submissions SET status='done'"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.conn.ExecContext(t.Context(), "UPDATE agents SET archived_at = 1 WHERE id = 'bot'"); err != nil {
		t.Fatal(err)
	}
	if err := r.service.Clear(t.Context(), "bot", chatID, "clear-1", "web", ""); !errors.Is(err, db.ErrAgentArchived) {
		t.Fatalf("Clear err = %v, want ErrAgentArchived", err)
	}
}

func TestUserArchivedParentReceivesNoChildResult(t *testing.T) {
	r, _, source := archiveFixture(t)
	ref, err := r.service.ToolSessionNew(t.Context(), source, engine.SessionNewArgs{Title: "Child", Message: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.conn.ExecContext(t.Context(), "UPDATE submissions SET status='done'"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.conn.ExecContext(t.Context(), "UPDATE agents SET archived_at = 1 WHERE id = 'bot'"); err != nil {
		t.Fatal(err)
	}
	var turn string
	if err := r.conn.QueryRowContext(t.Context(), "SELECT id FROM submissions WHERE parent_request_id IS NOT NULL").Scan(&turn); err != nil {
		t.Fatal(err)
	}
	tx, err := r.conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	parent, err := r.service.sendResult(t.Context(), r.q.WithTx(tx), turn, engine.Outcome{Status: "done", Answer: "ok"}, 0)
	if err != nil || parent != 0 {
		t.Fatalf("sendResult parent=%d err=%v for %v", parent, err, ref)
	}
	var results int
	if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE id LIKE 'result:%'").Scan(&results); err != nil || results != 0 {
		t.Fatalf("results=%d err=%v", results, err)
	}
}
