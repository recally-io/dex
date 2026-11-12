package chat

import (
	"sync"
	"testing"

	"github.com/recally-io/dex/internal/engine"
)

func TestReviewStopReplayDoesNotWithdrawLaterInput(t *testing.T) {
	r := startNative(t, "echo")
	_, source := taskParent(t, r)
	child, err := r.service.ToolSessionNew(t.Context(), source, engine.SessionNewArgs{Title: "Child", Message: "first"})
	if err != nil {
		t.Fatal(err)
	}
	stop := source
	stop.Key = "bot/stop-one"
	if _, err = r.service.ToolSessionStop(t.Context(), stop, child.ID); err != nil {
		t.Fatal(err)
	}
	send := source
	send.Key = "bot/send-after-stop"
	if _, err = r.service.ToolSessionSend(t.Context(), send, engine.SessionSendArgs{ID: child.ID, Message: "later work"}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.service.ToolSessionStop(t.Context(), stop, child.ID); err != nil {
		t.Fatal(err)
	}
	turn := receipt(t, r, turnID(send.Key))
	if turn.Status != "pending" {
		t.Fatalf("replayed Stop withdrew later input: status=%s reason=%s", turn.Status, turn.Reason.String)
	}
}

func TestReviewConcurrentForkReplayCreatesOneChild(t *testing.T) {
	r := startNative(t, "echo")
	parent, source := taskParent(t, r)
	child, err := r.service.ToolSessionNew(t.Context(), source, engine.SessionNewArgs{Title: "Child", Message: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.service.admit(t.Context(), taskInput(t, r, turnID(source.Key))); err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, turnID(source.Key))
	forkCall := source
	forkCall.Key = "bot/fork-one"
	start := make(chan struct{})
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			<-start
			_, err := r.service.ToolSessionFork(t.Context(), forkCall, engine.SessionForkArgs{ID: child.ID, Message: "fork work"})
			errors <- err
		})
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := r.conn.QueryRowContext(t.Context(), `SELECT count(*) FROM conversations WHERE parent_id=?`, parent).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("one source child and one fork expected; got %d children", count)
	}
}
