package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/durable"
)

func TestUserMainStopResumesNativeInboxAfterRestart(t *testing.T) {
	// Given a run with a follow-up whose execution is durable but has not started.
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	a := coreEngine(t, config)
	ref := coreCreate(t, a, "alpha", coreOptions("bash"))
	started := filepath.Join(config.DataDir, "started")
	first := coreSubmit(t, a, ref, "first", "touch '"+started+"'; exec sleep 60", "bash")
	coreFile(t, started)
	next := coreSubmit(t, a, ref, "next", "printf next", "bash")
	w, err := a.Watch(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	target := w.Snapshot.RunTarget
	w.Close()
	id, err := runTargetID(ref, target)
	if err != nil {
		t.Fatal(err)
	}

	// When the host stops after AbortTask but before the Inbox boundary.
	h, err := a.harness(t.Context(), ref.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := h.AbortTask(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := h.WaitForTask(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := coreWait(t, first); got.Status != "unanswered" || got.Reason != "aborted" {
		t.Fatalf("first run: %+v", got)
	}
	if record, err := next.native.Status(ctx); err != nil || record.Status != durable.SubmissionQueued {
		t.Fatalf("PiG AbortTask must preserve the idle Inbox: %+v, %v", record, err)
	}
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// Then replay of the durable stop intent starts the queued input once, with the same native ID.
	a = coreEngine(t, config)
	for range 2 {
		if err := a.StopRun(ctx, ref, target); err != nil {
			t.Fatal(err)
		}
	}
	replayed := coreSubmit(t, a, ref, "next", "printf different", "bash")
	if got := coreWait(t, replayed); replayed.ID != next.ID || got.Answer != "bash: next" {
		t.Fatalf("replayed native input: id=%d; outcome=%+v", replayed.ID, got)
	}
}
