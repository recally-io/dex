package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserTaskSteerIsScopedToParentAndRetryTarget(t *testing.T) {
	r := startNative(t, "echo")
	_, source := taskParent(t, r)
	first, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "One", Goal: "work"})
	if err != nil {
		t.Fatal(err)
	}
	source.Key = "bot/spawn-2"
	second, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Two", Goal: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.ReceiveTaskSteer(t.Context(), source, first.ID, "request", "text"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.ReceiveTaskSteer(t.Context(), source, second.ID, "request", "text"); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("target reuse: %v", err)
	}

	other, err := r.service.Create(t.Context(), "bot")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := r.service.execution(t.Context(), other.ID, "bot", engine.Options{Model: engine.ModelRef{Provider: "faux", ModelID: "echo"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: "other-source", ConversationID: other.ID, Content: "other", Attachments: "null", Origin: "web", Kind: "message", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	caller := engine.TaskContext{AgentID: "bot", ConversationID: ref.ID, SourceRequestID: "other-source"}
	if _, err := r.service.ReceiveTaskSteer(t.Context(), caller, first.ID, "new", "text"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("other parent wrote: %v", err)
	}
	if _, _, err := r.service.TaskSteerReceipt(t.Context(), caller, "request"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("other parent read: %v", err)
	}
}

func TestUserTaskSteerPendingSurvivesRestartAndCancellation(t *testing.T) {
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	task, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Work", Goal: "work"})
	if err != nil {
		t.Fatal(err)
	}
	sequence, err := r.service.ReceiveTaskSteer(t.Context(), source, task.ID, "stable", "text")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.agents.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	next, err := engine.New(engine.Config{DataDir: r.dir, Home: t.TempDir(), FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close(context.Background())
	service := New(r.conn, next, time.Now, nil, nil)
	defer service.cancel()
	if err := service.RecoverTasks(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, reason, err := service.TaskSteerReceipt(t.Context(), source, "stable")
	if err != nil || state != "pending" || reason != "" {
		t.Fatalf("restarted receipt: %q %q %v", state, reason, err)
	}
	if _, err := service.CancelTask(t.Context(), "bot", parent, task.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := service.ReceiveTaskSteer(t.Context(), source, task.ID, "stable", "text")
	if err != nil || replay != sequence {
		t.Fatalf("terminal replay: %d %v", replay, err)
	}
	state, reason, err = service.TaskSteerReceipt(t.Context(), source, "stable")
	if err != nil || state != "undelivered" || reason == "" {
		t.Fatalf("cancelled receipt: %q %q %v", state, reason, err)
	}
}

func TestUserTaskSteerAndCancellationHaveOneOrder(t *testing.T) {
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	task, err := r.service.SpawnTask(t.Context(), source, engine.SpawnTaskArgs{Title: "Work", Goal: "work"})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		sequence int64
		err      error
	}
	start := make(chan struct{})
	received := make(chan result, 1)
	cancelled := make(chan error, 1)
	go func() {
		<-start
		sequence, err := r.service.ReceiveTaskSteer(t.Context(), source, task.ID, "race", "text")
		received <- result{sequence, err}
	}()
	go func() {
		<-start
		_, err := r.service.CancelTask(t.Context(), "bot", parent, task.ID)
		cancelled <- err
	}()
	close(start)
	got := <-received
	if err := <-cancelled; err != nil {
		t.Fatal(err)
	}
	if errors.Is(got.err, ErrTaskConflict) {
		return
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	state, reason, err := r.service.TaskSteerReceipt(t.Context(), source, "race")
	if err != nil || state != "undelivered" || reason == "" {
		t.Fatalf("received before cancellation: %q %q %v", state, reason, err)
	}
	retry, err := r.service.ReceiveTaskSteer(t.Context(), source, task.ID, "race", "text")
	if err != nil || retry != got.sequence {
		t.Fatalf("retry: %d %v", retry, err)
	}
}
