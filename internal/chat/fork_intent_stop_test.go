package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/engine"
)

func TestUserStoppedInputCannotCreatePendingToolFork(t *testing.T) {
	r := startNative(t, "bash")
	started := filepath.Join(r.dir, "started")
	id, err := r.service.Post(t.Context(), "bot", MainKey, "", "request", "touch "+started+"; while :; do sleep 1; done")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(started); return err == nil })
	c, err := r.service.Get(t.Context(), "bot", id)
	if err != nil {
		t.Fatal(err)
	}
	lookup, err := r.agents.LookupSubmission(t.Context(), engine.Ref{AgentID: "bot", ID: c.ExecutionID}, "request")
	if err != nil || lookup.Entry == nil {
		t.Fatalf("lookup=%+v err=%v", lookup, err)
	}
	r.service.cancel()
	r.service.wg.Wait()
	if err = r.agents.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	source := engine.TaskContext{AgentID: "bot", ConversationID: c.ExecutionID, SourceRequestID: "request", Key: "bot/late"}
	if _, err = r.service.ToolFork(t.Context(), source, lookup.Entry.ID); !errors.Is(err, engine.ErrClosed) {
		t.Fatalf("expected failed native commit: %v", err)
	}
	if err = r.service.Stop(t.Context(), "bot", id); !errors.Is(err, engine.ErrClosed) {
		t.Fatalf("expected deferred abort: %v", err)
	}
	next, err := engine.New(engine.Config{DataDir: r.dir, Home: t.TempDir(), BaseEnv: map[string]string{"PATH": os.Getenv("PATH")}, FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close(context.Background())
	service := New(r.conn, next, time.Now, nil, nil)
	defer func() { service.cancel(); service.wg.Wait() }()
	if err = service.RecoverTasks(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = service.reconcileAgent(t.Context(), "bot"); err != nil {
		t.Fatal(err)
	}
	op, err := r.q.GetForkOperation(t.Context(), source.Key)
	if err != nil || op.Status != "rejected" {
		t.Fatalf("stopped fork created or stayed pending: %+v %v", op, err)
	}
}

func TestUserStoppedInputRecoversCommittedToolFork(t *testing.T) {
	r := startNative(t, "bash")
	started := filepath.Join(r.dir, "started")
	id, err := r.service.Post(t.Context(), "bot", MainKey, "", "request", "touch "+started+"; while :; do sleep 1; done")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(started); return err == nil })
	c, err := r.service.Get(t.Context(), "bot", id)
	if err != nil {
		t.Fatal(err)
	}
	lookup, err := r.agents.LookupSubmission(t.Context(), engine.Ref{AgentID: "bot", ID: c.ExecutionID}, "request")
	if err != nil || lookup.Entry == nil {
		t.Fatalf("lookup=%+v err=%v", lookup, err)
	}
	if _, err = r.conn.ExecContext(t.Context(), `CREATE TRIGGER fork_ack_failure BEFORE UPDATE OF execution_id ON conversations BEGIN SELECT RAISE(FAIL, 'fork ack unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	source := engine.TaskContext{AgentID: "bot", ConversationID: c.ExecutionID, SourceRequestID: "request", Key: "bot/saved"}
	if _, err = r.service.ToolFork(t.Context(), source, lookup.Entry.ID); err == nil {
		t.Fatal("expected acknowledgement failure")
	}
	if err = r.service.Stop(t.Context(), "bot", id); err != nil {
		t.Fatal(err)
	}
	if _, err = r.conn.ExecContext(t.Context(), `DROP TRIGGER fork_ack_failure`); err != nil {
		t.Fatal(err)
	}
	if err = r.service.reconcileAgent(t.Context(), "bot"); err != nil {
		t.Fatal(err)
	}
	op, err := r.q.GetForkOperation(t.Context(), source.Key)
	if err != nil || op.Status != "done" {
		t.Fatalf("committed fork lost: %+v %v", op, err)
	}
	child, err := r.service.ToolFork(t.Context(), source, lookup.Entry.ID)
	if err != nil || child != op.TargetConversationID {
		t.Fatalf("saved fork replay=%d err=%v", child, err)
	}
}
