package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/durable"
)

func TestUserLateTaskStopLeavesSuccessorRunning(t *testing.T) {
	t.Parallel()
	// Given a finished task and a successor running in the same session.
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	a := coreEngine(t, config)
	ref := coreCreate(t, a, "alpha", coreOptions("bash"))
	firstStarted := filepath.Join(config.DataDir, "first-started")
	firstRelease := filepath.Join(config.DataDir, "first-release")
	first := coreSubmit(t, a, ref, "first", "touch '"+firstStarted+"'; until test -f '"+firstRelease+"'; do sleep 0.01; done; printf first", "bash")
	coreFile(t, firstStarted)
	w, err := a.Watch(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	target := w.Snapshot.RunTarget
	w.Close()
	nextStarted := filepath.Join(config.DataDir, "next-started")
	nextRelease := filepath.Join(config.DataDir, "next-release")
	next := coreSubmit(t, a, ref, "next", "touch '"+nextStarted+"'; until test -f '"+nextRelease+"'; do sleep 0.01; done; printf next", "bash")
	if err := os.WriteFile(firstRelease, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	coreFile(t, nextStarted)

	// When the exact Stop arrives after the target has ended, it reports no effect.
	if err := a.StopTask(t.Context(), ref, target); !errors.Is(err, ErrStaleRunTarget) {
		t.Errorf("late Stop: got %v, want %v", err, ErrStaleRunTarget)
	}

	// Then neither the completed answer nor the successor's answer is lost.
	if err := os.WriteFile(nextRelease, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := coreWait(t, first); got.Answer != "bash: first" {
		t.Fatalf("first: %+v", got)
	}
	if got := coreWait(t, next); got.Answer != "bash: next" {
		t.Fatalf("successor: %+v", got)
	}
}

func TestUserMainStopResumesNativeInboxAfterRestart(t *testing.T) {
	t.Parallel()
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
	h, err := a.harness(t.Context(), ref.BotID)
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
		if err := a.StopTask(ctx, ref, target); !errors.Is(err, ErrStaleRunTarget) {
			t.Fatalf("replay of an already aborted task: %v", err)
		}
	}
	replayed := coreSubmit(t, a, ref, "next", "printf different", "bash")
	if got := coreWait(t, replayed); replayed.ID != next.ID || got.Answer != "bash: next" {
		t.Fatalf("replayed native input: id=%d; outcome=%+v", replayed.ID, got)
	}
}
