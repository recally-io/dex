package engine

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/durable"
)

func TestUserCompactionReusesOperationAndReportsNoOp(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	a := coreEngine(t, cfg)
	ref := coreCreate(t, a, "alpha", coreOptions("echo"))
	coreWait(t, coreSubmit(t, a, ref, "first", "short", "echo"))
	results := make(chan int64, 12)
	errs := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			id, err := a.Compact(t.Context(), ref, "same")
			if err != nil {
				errs <- err
				return
			}
			results <- id
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var id int64
	for got := range results {
		if id != 0 && got != id {
			t.Fatalf("duplicate tasks %d %d", id, got)
		}
		id = got
	}
	if id == 0 {
		t.Fatal("no task")
	}
	conv, err := a.conversation(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conv.Commit(t.Context(), func(tx durable.Tx) (any, error) {
		kind := "pi.compaction"
		page, err := tx.ScanTasks(durable.TaskQuery{ConversationId: new(conv.Id()), Kind: &kind}, 100, nil)
		if err != nil {
			return nil, err
		}
		if len(page.Items) != 1 {
			return nil, fmt.Errorf("compaction tasks=%d", len(page.Items))
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		state, err := a.CompactionStatus(ctx, ref, id)
		if err != nil {
			t.Fatal(err)
		}
		if state.Status == "no_op" {
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("last state: %+v", state)
		case <-tick.C:
		}
	}
	other := coreCreate(t, a, "alpha", coreOptions("echo"))
	if _, err := a.CompactionStatus(ctx, other, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign task: %v", err)
	}
	beta := coreCreate(t, a, "beta", coreOptions("echo"))
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	a = coreEngine(t, cfg)
	if _, found, err := a.LookupCompact(ctx, ref, "absent"); err != nil || found || len(a.resources) != 0 {
		t.Fatalf("cold absent found=%v err=%v", found, err)
	}
	cold, found, err := a.LookupCompact(ctx, ref, "same")
	if err != nil || !found || cold != id || len(a.resources) != 0 {
		t.Fatalf("cold lookup=%d found=%v err=%v", cold, found, err)
	}
	if _, err := a.conversation(ctx, beta); err != nil {
		t.Fatal(err)
	}
	cold, found, err = a.LookupCompact(ctx, ref, "same")
	if err != nil || !found || cold != id || a.resources[ref.AgentID] != nil {
		t.Fatalf("cold lookup with another agent=%d found=%v err=%v", cold, found, err)
	}
	got, err := a.Compact(ctx, ref, "same")
	if err != nil || got != id {
		t.Fatalf("reopen id=%d err=%v", got, err)
	}
	t.Logf("compaction %d returned no_op and survived replay", id)
}
