package engine

import (
	"errors"
	"sync"
	"testing"

	"github.com/MichaelKinsy/PiG/durable"
)

func TestUserForkIsIdempotentAndChecksEntry(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	a := coreEngine(t, cfg)
	ref := coreCreate(t, a, "alpha", coreOptions("echo"))
	coreWait(t, coreSubmit(t, a, ref, "first", "hello", "echo"))
	conv, err := a.conversation(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	var entry durable.EntryId
	_, err = conv.Commit(t.Context(), func(tx durable.Tx) (any, error) {
		id, err := tx.LatestEntryId(conv.Id())
		if err != nil {
			return nil, err
		}
		if id == nil {
			t.Fatal("entry missing")
		}
		entry = *id
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	opts := coreOptions("shout")
	results := make(chan Ref, 12)
	errs := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			child, err := a.Fork(t.Context(), ref, "op", int64(entry), opts)
			if err != nil {
				errs <- err
				return
			}
			results <- child
		})
	}
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Error(err)
	}
	var child Ref
	for got := range results {
		if child.ID != 0 && got != child {
			t.Fatalf("duplicate child: %+v %+v", child, got)
		}
		child = got
	}
	if child.ID == 0 {
		t.Fatal("no child")
	}
	if _, err := a.Fork(t.Context(), ref, "op", int64(entry), coreOptions("echo")); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("different arguments: %v", err)
	}
	other := coreCreate(t, a, "alpha", coreOptions("echo"))
	if _, err := a.Fork(t.Context(), other, "op", int64(entry), opts); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign entry: %v", err)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	a = coreEngine(t, cfg)
	got, err := a.Fork(t.Context(), ref, "op", int64(entry), opts)
	if err != nil || got != child {
		t.Fatalf("restart got=%+v err=%v", got, err)
	}
}
