package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/engine"
)

func TestUserContextOperationReportsPendingCancellationDuringMaintenance(t *testing.T) {
	r := startNative(t, "echo")
	id, err := r.service.Post(t.Context(), "bot", MainKey, "", "message", "hello")
	if err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "message")
	c, err := r.service.Get(t.Context(), "bot", id)
	if err != nil {
		t.Fatal(err)
	}
	lookup, err := r.agents.LookupSubmission(t.Context(), engine.Ref{AgentID: "bot", ID: c.ExecutionID}, "message")
	if err != nil || lookup.Entry == nil {
		t.Fatalf("lookup=%+v err=%v", lookup, err)
	}
	if err = r.q.RequestExecutionAbort(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err = r.agents.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	next, err := engine.New(engine.Config{DataDir: r.dir, Home: t.TempDir(), FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close(context.Background())
	if err = next.Quiesce(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer next.Resume()
	service := New(r.conn, next, time.Now, nil, nil)
	defer func() { service.cancel(); service.wg.Wait() }()
	if _, err = service.Fork(t.Context(), "bot", id, lookup.Entry.ID, "fork"); !errors.Is(err, ErrCancellationPending) || errors.Is(err, engine.ErrModelsPending) {
		t.Fatalf("expected pending cancellation, got %v", err)
	}
}
