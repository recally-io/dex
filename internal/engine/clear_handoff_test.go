package engine

import (
	"errors"
	"strings"
	"testing"
)

func TestUserResetHandoffDoesNotRunAndRejectsChangedReplay(t *testing.T) {
	a := coreEngine(t, Config{})
	ref := coreCreate(t, a, "alpha", coreOptions("context"))
	coreWait(t, coreSubmit(t, a, ref, "before", "old", "context"))
	if err := a.ResetWithHandoff(t.Context(), ref, "reset", "retain"); err != nil {
		t.Fatal(err)
	}
	if err := a.ResetWithHandoff(t.Context(), ref, "reset", "retain"); err != nil {
		t.Fatal(err)
	}
	if err := a.ResetWithHandoff(t.Context(), ref, "reset", "different"); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("changed handoff: %v", err)
	}
	if err := a.CheckIdle(t.Context(), ref); err != nil {
		t.Fatalf("reset started work: %v", err)
	}
	out := coreWait(t, coreSubmit(t, a, ref, "after", "next", "context"))
	if !strings.Contains(out.Answer, "retain") || !strings.Contains(out.Answer, "next") || strings.Contains(out.Answer, "old") {
		t.Fatalf("context: %+v", out)
	}
}
