package chat

import (
	"context"
	"errors"
	"testing"
)

func TestUserSourceReceiptBelongsToItsAgentBeforeAdmission(t *testing.T) {
	// Given an agent whose native admission is held for maintenance.
	r := startNative(t, "echo")
	if err := r.service.AcquireMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.service.ReleaseMaintenance(t.Context()); err != nil {
			t.Error(err)
		}
	})
	absent, err := r.service.LookupSource(t.Context(), "bot", "missing")
	if err != nil || absent.ID != "" {
		t.Fatalf("absent source=%+v err=%v", absent, err)
	}
	// When a channel stores an input, its business receipt exists before admission.
	if err := r.service.SubmitMessage(t.Context(), "bot", "telegram:7", "owned-source", Message{Content: "stored input"}); err != nil {
		t.Fatal(err)
	}
	stored, err := r.service.LookupSource(t.Context(), "bot", "owned-source")
	if err != nil || stored.ID == "" || stored.ChatID == 0 || !stored.Pending {
		t.Fatalf("stored source=%+v err=%v", stored, err)
	}
	// Then another agent cannot use that receipt or its preview reference.
	if foreign, err := r.service.LookupSource(t.Context(), "other", "owned-source"); !errors.Is(err, ErrNotFound) || foreign.ID != "" {
		t.Fatalf("foreign source=%+v err=%v", foreign, err)
	}
	// Receipt existence is global, but it returns no agent or preview reference.
	if received, err := r.service.ReceivedSource(t.Context(), "owned-source"); err != nil || !received {
		t.Fatalf("global source received=%v err=%v", received, err)
	}
	if received, err := r.service.ReceivedSource(t.Context(), "missing"); err != nil || received {
		t.Fatalf("missing global source received=%v err=%v", received, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if received, err := r.service.ReceivedSource(ctx, "owned-source"); received || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled global lookup received=%v err=%v", received, err)
	}
	if _, err := r.service.LookupSource(ctx, "bot", "owned-source"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lookup=%v", err)
	}
	// Stopping the input keeps the receipt but removes the pending preview state.
	if err := r.service.Stop(t.Context(), "bot", stored.ChatID); err != nil {
		t.Fatal(err)
	}
	stopped, err := r.service.LookupSource(t.Context(), "bot", "owned-source")
	if err != nil || stopped.ID != stored.ID || stopped.ChatID != stored.ChatID || stopped.Pending {
		t.Fatalf("stopped source=%+v err=%v", stopped, err)
	}
}
