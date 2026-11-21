package worktask

import (
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserTracksWorkAcrossTaskLifecycle(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	conn, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := sqlc.New(conn).CreateAgent(ctx, sqlc.CreateAgentParams{ID: "default", Name: "Dex", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	s := New(conn)
	task, err := s.Create(ctx, "default", "Fix login")
	if err != nil || task.Lifecycle != "open" {
		t.Fatalf("create: %+v, %v", task, err)
	}
	if _, err := s.Append(ctx, "default", task.ID, "PR is ready for review"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetLifecycle(ctx, "default", task.ID, "completed"); err != nil {
		t.Fatal(err)
	}
	open, err := s.List(ctx, false)
	if err != nil || len(open) != 0 {
		t.Fatalf("open tasks: %+v, %v", open, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	detail, err := New(other).Detail(ctx, "default", task.ID)
	if err != nil || detail.Task.Lifecycle != "completed" || len(detail.Entries) != 1 || detail.Entries[0].Content != "PR is ready for review" {
		t.Fatalf("reopened database: %+v, %v", detail, err)
	}
}
