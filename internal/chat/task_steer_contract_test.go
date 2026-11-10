package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/recally-io/dex/internal/engine"
)

// This deliberately narrow proposed boundary leaves native placement and delivery
// reconciliation to the caller. It is not a user-facing task tool.
type taskSteerContract interface {
	ReceiveTaskSteer(context.Context, engine.TaskContext, string, string, string) (int64, error)
	TaskSteerReceipt(context.Context, engine.TaskContext, string) (string, string, error)
}

func steerContract(t *testing.T, r *nativeTest) taskSteerContract {
	t.Helper()
	service, ok := any(r.service).(taskSteerContract)
	if !ok {
		t.Fatal("task steer has no durable receive and receipt boundary")
	}
	return service
}

func TestUserTaskSteerRetryAndCancellationReceipt(t *testing.T) {
	// Given a task owned by the caller, accepted intents retain their order.
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	task, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Work", Goal: "work"})
	if err != nil {
		t.Fatal(err)
	}
	s := steerContract(t, r)
	first, err := s.ReceiveTaskSteer(t.Context(), source, task.ID, "request-1", "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.ReceiveTaskSteer(t.Context(), source, task.ID, "request-2", "second")
	if err != nil || second <= first {
		t.Fatalf("receipt ordering: first=%d second=%d err=%v", first, second, err)
	}
	retry, err := s.ReceiveTaskSteer(t.Context(), source, task.ID, "request-1", "first")
	if err != nil || retry != first {
		t.Fatalf("retry changed receipt: first=%d retry=%d err=%v", first, retry, err)
	}
	if _, err := s.ReceiveTaskSteer(t.Context(), source, task.ID, "request-1", "different"); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("different body under same request was not rejected: %v", err)
	}
	// When cancellation wins, an intent that never reached the executor has a reason.
	if _, err := r.service.CancelTask(t.Context(), "bot", parent, task.ID); err != nil {
		t.Fatal(err)
	}
	state, reason, err := s.TaskSteerReceipt(t.Context(), source, "request-1")
	if err != nil || state != "undelivered" || reason == "" {
		t.Fatalf("cancelled intent: state=%q reason=%q err=%v", state, reason, err)
	}
	if _, err := s.ReceiveTaskSteer(t.Context(), source, task.ID, "request-3", "late"); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("terminal task accepted a new intent: %v", err)
	}
}

func TestUserTaskSteerRejectsOtherParent(t *testing.T) {
	// Given a task from another chat, its ID does not grant permission to steer it.
	r := startNative(t, "echo")
	_, source := taskParent(t, r)
	task, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Work", Goal: "work"})
	if err != nil {
		t.Fatal(err)
	}
	s := steerContract(t, r)
	other := source
	other.AgentID = "other"
	if _, err := s.ReceiveTaskSteer(t.Context(), other, task.ID, "request", "text"); err == nil {
		t.Fatal("another agent steered the task")
	}
}
