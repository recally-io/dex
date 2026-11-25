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
	if _, err := s.Update(ctx, call("alert", "rename"), WorkTaskUpdateArgs{ID: task.ID, Title: "From an alert"}); !errors.Is(err, worktask.ErrForbidden) {
		t.Fatalf("a Task alert rename error = %v, want ErrForbidden", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO conversations (id, agent_id, channel_key, created_at, parent_id)
		VALUES (2, 'default', 'chat:child', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, call("alert", "link"), WorkTaskUpdateArgs{ID: task.ID, Session: "2"}); !errors.Is(err, worktask.ErrForbidden) {
		t.Fatalf("a Task alert link error = %v, want ErrForbidden", err)
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

	// A user can create a Task from a top-level Chat, but a child cannot act as that Chat.
	if _, err := conn.ExecContext(ctx, `INSERT INTO conversations (id, agent_id, channel_key, execution_id, created_at, parent_id)
		VALUES (3, 'default', 'chat:standalone', 8, 1, NULL), (4, 'default', 'chat:nested', 9, 1, 3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO submissions (id, conversation_id, content, status, created_at, kind, origin, producer)
		VALUES ('chat', 3, 'x', 'pending', 1, 'message', 'web', 'user'), ('child', 4, 'x', 'pending', 1, 'message', 'web', 'user')`); err != nil {
		t.Fatal(err)
	}
	standalone, child := call("chat", "chat"), call("child", "child")
	standalone.ConversationID, child.ConversationID = 8, 9
	created, err := s.New(ctx, standalone, WorkTaskNewArgs{Title: "From a Chat"})
	if err != nil {
		t.Fatalf("top-level Chat could not create a Task: %v", err)
	}
	if _, err := s.New(ctx, child, WorkTaskNewArgs{Title: "From a child"}); !errors.Is(err, worktask.ErrForbidden) {
		t.Fatalf("child create error = %v, want ErrForbidden", err)
	}
	if list, err := s.List(ctx, child, WorkTaskListArgs{}); err != nil || len(list) != 0 {
		t.Fatalf("unlinked child list = %+v, %v; want no Tasks", list, err)
	}
	standalone.Key += "/link"
	if _, err := s.Update(ctx, standalone, WorkTaskUpdateArgs{ID: created.ID, Session: "4"}); err != nil {
		t.Fatalf("top-level Chat could not link its child: %v", err)
	}
	if list, err := s.List(ctx, child, WorkTaskListArgs{}); err != nil || len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("linked child list = %+v, %v; want only %s", list, err, created.ID)
	}
}

func TestUserWokenMainReadsAndUpdatesTaskByKey(t *testing.T) {
	ctx := t.Context()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := sqlc.New(conn).CreateAgent(ctx, sqlc.CreateAgentParams{ID: "default", Name: "Dex", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO conversations (id, agent_id, channel_key, execution_id, created_at) VALUES (1, 'default', 'main', 7, 1)`); err != nil {
		t.Fatal(err)
	}
	tasks := worktask.New(conn, nil)
	created, err := tasks.Create(ctx, "default", "Ship board wake")
	if err != nil {
		t.Fatal(err)
	}
	key := worktask.FormatKey(created.Number)
	if _, err := conn.ExecContext(ctx, `INSERT INTO submissions (id, conversation_id, content, status, created_at, kind, origin, producer)
		VALUES ('wake-1', 1, '{"highWaterEntryId":1,"items":[{"taskKey":"T-1","reason":"assigned"}]}', 'pending', 2, 'task_wake', 'web', 'plugin')`); err != nil {
		t.Fatal(err)
	}
	s := New(conn)
	s.ConfigureWorkTasks(tasks)
	call := func(op string) engine.TaskContext {
		return engine.TaskContext{AgentID: "default", ConversationID: 7, SourceRequestID: "wake-1", Key: "default/" + op}
	}

	// Given a Board wake naming only T-1, Main resolves and acts on the Task by key.
	listed, err := s.List(ctx, call("list"), WorkTaskListArgs{})
	if err != nil || len(listed) != 1 || listed[0].Key != key {
		t.Fatalf("task_list = %+v, %v; want key %s", listed, err, key)
	}
	detail, err := s.Get(ctx, call("get"), key)
	if err != nil || detail.ID != created.ID || detail.Key != key {
		t.Fatalf("task_get(%s) = %+v, %v", key, detail, err)
	}
	if _, err := s.Append(ctx, call("append"), WorkTaskAppendArgs{ID: key, Content: "Checked from wake"}); err != nil {
		t.Fatalf("task_append(%s): %v", key, err)
	}
	renamed, err := s.Update(ctx, call("rename"), WorkTaskUpdateArgs{ID: key, Title: "Shipped board wake"})
	if err != nil || renamed.Title != "Shipped board wake" || renamed.Key != key {
		t.Fatalf("task_update(%s) = %+v, %v", key, renamed, err)
	}
}
