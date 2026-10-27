package engine

import (
	"context"
	"testing"
	"time"
)

func TestUserBackgroundActivityTracksWithoutUserWatch(t *testing.T) {
	// Given a background execution with a real faux bash tool and no transcript subscriber.
	a := coreEngine(t, Config{})
	ref := coreCreate(t, a, "alpha", coreOptions("bash"))
	sub, err := a.Submit(t.Context(), ref, Input{
		RequestID: "activity", Content: "printf activity; while :; do sleep 1; done", Kind: "message",
		Origin: "task", Source: "dex:alpha/task/activity", Background: true, Options: coreOptions("bash"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	var activity Activity
	for {
		activity = a.Activity([]Ref{ref})[ref]
		if activity.LastAction != "" && activity.LastActivityAt != nil && activity.ToolStartedAt != nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("activity did not update: %+v", activity)
		case <-tick.C:
		}
	}
	if activity.LastAction != "bash: printf activity; while :; do sleep 1; done" {
		t.Fatalf("last action: %q", activity.LastAction)
	}
	// When admission retries the same request, it reuses the running watch.
	replay, err := a.Submit(t.Context(), ref, Input{
		RequestID: "activity", Content: "printf activity; while :; do sleep 1; done", Kind: "message",
		Origin: "task", Source: "dex:alpha/task/activity", Background: true, Options: coreOptions("bash"),
	})
	if err != nil || replay.ID != sub.ID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	a.mu.Lock()
	watches := a.activityWatches[ref]
	a.mu.Unlock()
	if watches != 1 {
		t.Fatalf("replay started another activity watch: %d", watches)
	}
	_ = a.Abort(t.Context(), ref)
	for _, s := range []*Submission{sub, replay} {
		if _, err := s.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := a.Activity([]Ref{ref}); len(got) != 0 {
		t.Fatalf("activity remained after settlement: %+v", got)
	}
	a.mu.Lock()
	watches = len(a.activityWatches)
	a.mu.Unlock()
	if watches != 0 {
		t.Fatalf("activity watches remained after settlement: %d", watches)
	}
}
