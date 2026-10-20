package chat

import (
	"context"
	"testing"

	"github.com/recally-io/dex/internal/engine"
)

func TestUserCancelledTaskInvocationCannotCreateWork(t *testing.T) {
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.service.SpawnTask(ctx, source, engine.SpawnTaskArgs{Title: "Expired", Goal: "must not run"}); err == nil {
		t.Fatal("cancelled invocation created a task")
	}
	tasks, err := r.service.ListTasks(t.Context(), "bot", parent)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("tasks after cancellation: %+v %v", tasks, err)
	}
}
