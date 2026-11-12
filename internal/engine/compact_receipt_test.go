package engine

import (
	"context"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/durable"
)

func TestUserNativeCompactReceiptRunsAndReplaysOnce(t *testing.T) {
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	a := coreEngine(t, config)
	ref := coreCreate(t, a, "alpha", coreOptions("echo"))
	coreWait(t, coreSubmit(t, a, ref, "input", "hello", "echo"))
	conversation, err := a.conversation(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	first, err := compactOnce(t.Context(), conversation, durable.ConversationId(ref.ID), "same", "keep")
	if err != nil || !first.created {
		t.Fatalf("first admission = %+v, %v", first, err)
	}
	second, err := compactOnce(t.Context(), conversation, durable.ConversationId(ref.ID), "same", "keep")
	if err != nil || second.created || second.id != first.id {
		t.Fatalf("replay = %+v, %v", second, err)
	}
	if _, err := compactOnce(t.Context(), conversation, durable.ConversationId(ref.ID), "same", "changed"); err == nil {
		t.Fatal("changed instructions reused the native compact ID")
	}
	h, err := a.harness(t.Context(), ref.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	task, err := h.WaitForTask(ctx, first.id)
	if err != nil || task.State.Outcome == nil {
		t.Fatalf("task outcome = %+v, %v", task.State.Outcome, err)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened := coreEngine(t, config)
	conversation, err = reopened.conversation(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	afterRestart, err := compactOnce(t.Context(), conversation, durable.ConversationId(ref.ID), "same", "keep")
	if err != nil || afterRestart.created || afterRestart.id != first.id {
		t.Fatalf("restart replay = %+v, %v", afterRestart, err)
	}
}
