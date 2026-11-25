package tools

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/worktask"
)

func TestUserAloneChangesTaskLifecycle(t *testing.T) {
	ctx := t.Context()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := sqlc.New(conn).CreateAgent(ctx, sqlc.CreateAgentParams{ID: "default", Name: "Dex", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	// Given Main with one input from the owner, one result of a child session, and one Task alert.
	// A Task alert has the web origin of Main, so only its kind separates it from the owner.
	if _, err := conn.ExecContext(ctx, `INSERT INTO conversations (id, agent_id, channel_key, execution_id, created_at) VALUES (1, 'default', 'main', 7, 1)`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, kind, origin, producer string }{
		{"owner", "message", "web", chat.ProducerUser},
		{"result", "task_result", "task", chat.ProducerSubSession},
		{"alert", "task_result", "web", chat.ProducerPlugin},
	} {
		if _, err := conn.ExecContext(ctx, `INSERT INTO submissions (id, conversation_id, content, status, created_at, kind, origin, producer) VALUES (?, 1, 'x', 'pending', 1, ?, ?, ?)`, row.id, row.kind, row.origin, row.producer); err != nil {
			t.Fatal(err)
		}
	}
	s := New(conn)
	tasks := worktask.New(conn, nil)
	s.ConfigureWorkTasks(tasks)
	call := func(source, key string) engine.TaskContext {
		return engine.TaskContext{AgentID: "default", ConversationID: 7, SourceRequestID: source, Key: "default/" + key}
	}
	task, err := s.New(ctx, call("owner", "1"), WorkTaskNewArgs{Title: "Ship login"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.New(ctx, call("result", "2"), WorkTaskNewArgs{Title: "From a result"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a session result created a Task: %v", err)
	}
	if _, err := s.New(ctx, call("alert", "2a"), WorkTaskNewArgs{Title: "From an alert"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a Task alert created a Task: %v", err)
	}
	if _, err := s.Update(ctx, call("alert", "2b"), WorkTaskUpdateArgs{ID: task.ID, Lifecycle: "cancelled"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a Task alert cancelled the Task: %v", err)
	}
	var notified []int64
	tasks.SetOnEntry(func(id int64) { notified = append(notified, id) })
	if _, err := s.Append(ctx, call("alert", "2c"), WorkTaskAppendArgs{ID: task.ID, Content: "Note from a source alert"}); !errors.Is(err, worktask.ErrForbidden) {
		t.Fatalf("a Task alert append error = %v, want task action not allowed", err)
	}

	// When a session result tries to complete the Task, the change is refused, but it can record progress.
	if _, err := s.Update(ctx, call("result", "3"), WorkTaskUpdateArgs{ID: task.ID, Lifecycle: "completed"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a session result completed the Task: %v", err)
	}
	entry, err := s.Append(ctx, call("result", "4"), WorkTaskAppendArgs{ID: task.ID, Content: "Child finished"})
	if err != nil {
		t.Fatal(err)
	}

	// Then a replayed append adds one entry, and the owner's request completes the Task.
	if replay, err := s.Append(ctx, call("result", "4"), WorkTaskAppendArgs{ID: task.ID, Content: "Child finished"}); err != nil || replay != entry {
		t.Fatalf("append replay = %+v, %v; want %+v", replay, err, entry)
	}
	if len(notified) != 1 || notified[0] != entry.ID {
		t.Fatalf("expected one notification for entry %d, got %v", entry.ID, notified)
	}
	got, err := s.Get(ctx, call("owner", "5"), task.ID)
	if err != nil || len(got.Entries) != 1 || got.Lifecycle != "open" {
		t.Fatalf("expected one entry on the open Task: %+v, %v", got, err)
	}
	done, err := s.Update(ctx, call("owner", "6"), WorkTaskUpdateArgs{ID: task.ID, Lifecycle: "completed"})
	if err != nil || done.Lifecycle != "completed" {
		t.Fatalf("the owner could not complete the Task: %+v, %v", done, err)
	}
}
