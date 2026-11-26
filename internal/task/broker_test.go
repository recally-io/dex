package task_test

import (
	"testing"

	tasksvc "github.com/recally-io/dex/internal/task"
)

func TestUserReceivesBoardHintsWithAnIndependentSlowSubscriber(t *testing.T) {
	t.Parallel()
	// Given one idle subscriber and one that keeps up.
	var broker tasksvc.Broker
	idle, closeIdle := broker.Subscribe()
	defer closeIdle()
	active, closeActive := broker.Subscribe()
	defer closeActive()

	// When the idle subscriber's buffer fills, the active subscriber still gets hints.
	for id := int64(1); id <= 32; id++ {
		broker.Publish(id)
		if got := <-active; got != id {
			t.Fatalf("hint = %d, want %d", got, id)
		}
	}

	// Then cancelling one subscription leaves the other open.
	if got := len(idle); got != 16 {
		t.Fatalf("idle buffer holds %d hints, want 16", got)
	}
	closeIdle()
	broker.Publish(33)
	if got := <-active; got != 33 {
		t.Fatalf("active hint = %d, want 33", got)
	}
}
