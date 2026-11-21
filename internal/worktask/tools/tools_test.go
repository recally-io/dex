package tools

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
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
	for _, row := range [][]string{{"owner", "message", "web"}, {"result", "task_result", "task"}, {"alert", "task_result", "web"}} {
		if _, err := conn.ExecContext(ctx, `INSERT INTO submissions (id, conversation_id, content, status, created_at, kind, origin) VALUES (?, 1, 'x', 'pending', 1, ?, ?)`, row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}
	s := New(conn)
	call := func(source, key string) engine.TaskContext {
		return engine.TaskContext{AgentID: "default", ConversationID: 7, SourceRequestID: source, Key: "default/" + key}
	}
	task, err := s.New(ctx, call("owner", "1"), engine.WorkTaskNewArgs{Title: "Ship login"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.New(ctx, call("result", "2"), engine.WorkTaskNewArgs{Title: "From a result"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a session result created a Task: %v", err)
	}
	if _, err := s.New(ctx, call("alert", "2a"), engine.WorkTaskNewArgs{Title: "From an alert"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a Task alert created a Task: %v", err)
	}
	if _, err := s.Update(ctx, call("alert", "2b"), engine.WorkTaskUpdateArgs{ID: task.ID, Lifecycle: "cancelled"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a Task alert cancelled the Task: %v", err)
	}

	// When a session result tries to complete the Task, the change is refused, but it can record progress.
	if _, err := s.Update(ctx, call("result", "3"), engine.WorkTaskUpdateArgs{ID: task.ID, Lifecycle: "completed"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a session result completed the Task: %v", err)
	}
	if _, err := s.Append(ctx, call("result", "4"), engine.WorkTaskAppendArgs{ID: task.ID, Content: "Child finished"}); err != nil {
		t.Fatal(err)
	}

	// Then a replayed append adds one entry, and the owner's request completes the Task.
	if _, err := s.Append(ctx, call("result", "4"), engine.WorkTaskAppendArgs{ID: task.ID, Content: "Child finished"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, call("owner", "5"), task.ID)
	if err != nil || len(got.Entries) != 1 || got.Lifecycle != "open" {
		t.Fatalf("expected one entry on the open Task: %+v, %v", got, err)
	}
	done, err := s.Update(ctx, call("owner", "6"), engine.WorkTaskUpdateArgs{ID: task.ID, Lifecycle: "completed"})
	if err != nil || done.Lifecycle != "completed" {
		t.Fatalf("the owner could not complete the Task: %+v, %v", done, err)
	}
}
