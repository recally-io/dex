package engine

import (
	"context"
	"testing"
	"time"
)

func TestUserNativeCompactReceiptRunsAndReplaysOnce(t *testing.T) {
	t.Parallel()
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	a := coreEngine(t, config)
	ref := coreCreate(t, a, "alpha", coreOptions("echo"))
	coreWait(t, coreSubmit(t, a, ref, "input", "hello", "echo"))
	if err := a.Compact(t.Context(), ref, "keep", "same", coreOptions("echo")); err != nil {
		t.Fatalf("first compact: %v", err)
	}
	if err := a.Compact(t.Context(), ref, "keep", "same", coreOptions("echo")); err != nil {
		t.Fatalf("replay compact: %v", err)
	}
	if err := a.Compact(t.Context(), ref, "changed", "same", coreOptions("echo")); err == nil {
		t.Fatal("changed instructions reused the native compact ID")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		history, err := a.History(ctx, ref, 1<<60, 100)
		if err != nil {
			t.Fatal(err)
		}
		settled := 0
		for _, item := range history.Items {
			if item.Boundary == "compaction" && item.Status != "pending" {
				settled++
			}
		}
		if settled > 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("compaction did not settle: %+v", history.Items)
		case <-tick.C:
		}
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened := coreEngine(t, config)
	if err := reopened.Compact(t.Context(), ref, "keep", "same", coreOptions("echo")); err != nil {
		t.Fatalf("restart replay compact: %v", err)
	}
	history, err := reopened.History(t.Context(), ref, 1<<60, 100)
	if err != nil {
		t.Fatal(err)
	}
	compactions := 0
	for _, item := range history.Items {
		if item.Boundary == "compaction" {
			compactions++
		}
	}
	if compactions != 1 {
		t.Fatalf("compaction items = %d; history=%+v", compactions, history.Items)
	}
}
