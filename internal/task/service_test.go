package task

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
	s := New(conn, nil)
	task, err := s.CreateTask(ctx, Owner, OriginUser, CreateTaskParams{AssigneeBotID: "default", Title: "Fix login"})
	if err != nil || task.Status != StatusReady {
		t.Fatalf("create: %+v, %v", task, err)
	}
	if _, err := s.PostEntry(ctx, Owner, OriginUser, task.ID, PostEntryParams{Content: "PR is ready for review"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(ctx, task.ID, UpdateParams{Status: new(StatusCancelled)}); err != nil {
		t.Fatal(err)
	}
	open, err := s.ListTasks(ctx, Filter{})
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
	detail, err := New(other, nil).Detail(ctx, task.ID)
	if err != nil || detail.Task.Status != StatusCancelled || len(detail.Entries) != 3 || detail.Entries[1].Content != "PR is ready for review" {
		t.Fatalf("reopened database: %+v, %v", detail, err)
	}
}
