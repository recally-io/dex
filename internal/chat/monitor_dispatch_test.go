package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserMonitorScriptWakeOnlyOnChangeAndTerminal(t *testing.T) {
	r := startNative(t, "echo")
	id, source := taskParent(t, r)
	now := r.service.now()
	r.service.now = func() time.Time { return now }
	cwd := t.TempDir()
	if _, err := r.service.SetProject(t.Context(), "bot", id, cwd); err != nil {
		t.Fatal(err)
	}
	value := filepath.Join(cwd, "reading")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(value, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("waiting")
	// Given a synthetic local status file. The check cannot find it unless it uses the session cwd.
	info, err := r.service.ToolMonitorSet(t.Context(), source, engine.MonitorSetArgs{DelaySeconds: 60, IntervalSeconds: 60, MaxRuns: 10, Prompt: "Read the fixture's status", CheckCommand: `cat ./reading || exit 2; if [ "$(cat ./reading)" = done ]; then exit 10; fi`})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		reading, outcome string
		event            bool
	}{
		{"waiting", "baseline recorded", false}, {"waiting", "no change", false}, {"updated", "check changed", true}, {"done", "terminal check", true},
	} {
		write(want.reading)
		now = now.Add(time.Minute)
		if err := r.service.FireDueMonitors(t.Context()); err != nil {
			t.Fatal(err)
		}
		got := listedMonitor(t, r, id, info.ID)
		if got.Outcome != want.outcome {
			t.Fatalf("check %d: got %+v, want outcome %q", got.Runs, got, want.outcome)
		}
		eventID := fmt.Sprintf("monitor:%s:%d", info.ID, got.Runs)
		event, err := r.q.GetSubmission(t.Context(), eventID)
		if !want.event && !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("quiet check queued an event: %v", err)
		}
		if want.event {
			if err != nil || event.WhenBusy != "followUp" || strings.Contains(event.Content, want.reading) {
				t.Fatalf("expected a follow-up without raw output: %+v, %v", event, err)
			}
			if err := r.service.admit(t.Context(), taskInput(t, r, eventID)); err != nil {
				t.Fatal(err)
			}
			awaitReceipt(t, r, eventID)
			if receipt, err := r.q.GetSubmission(t.Context(), eventID); err != nil || receipt.Status != "done" {
				t.Fatalf("native receipt: %+v, %v", receipt, err)
			}
		}
	}
	now = now.Add(time.Hour)
	if err := r.service.FireDueMonitors(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := listedMonitor(t, r, id, info.ID); got.State != "fired" || got.Runs != 4 {
		t.Fatalf("terminal check did not stop: %+v", got)
	}
}

func TestUserMonitorStopsAtEitherLimitAndShowsCheckFailures(t *testing.T) {
	r := startNative(t, "echo")
	id, source := taskParent(t, r)
	now := r.service.now()
	r.service.now = func() time.Time { return now }
	cases := []struct {
		key, state, outcome, cmd string
		max, runs                int
		expiry                   time.Duration
	}{
		{key: "count", max: 2, runs: 2, state: "fired", outcome: "prompt queued"},
		{key: "age", max: 3, runs: 1, expiry: 90 * time.Second, state: "expired", outcome: "time limit reached", cmd: `printf ready`},
		{key: "exit", max: 3, runs: 1, state: "cancelled", outcome: "check failed: check exited with code 3", cmd: `exit 3`},
	}
	ids := make(map[string]string)
	for _, c := range cases {
		source.Key = c.key
		args := engine.MonitorSetArgs{DelaySeconds: 60, IntervalSeconds: 60, MaxRuns: c.max, Prompt: "bounded check", CheckCommand: c.cmd}
		if c.expiry != 0 {
			args.ExpiresAt = now.Add(c.expiry).Format(time.RFC3339Nano)
		}
		info, err := r.service.ToolMonitorSet(t.Context(), source, args)
		if err != nil {
			t.Fatal(err)
		}
		ids[c.key] = info.ID
	}
	for range 3 {
		now = now.Add(time.Minute)
		if err := r.service.FireDueMonitors(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range cases {
		got := listedMonitor(t, r, id, ids[c.key])
		if got.State != c.state || got.Outcome != c.outcome || got.Runs != c.runs {
			t.Errorf("%s: got %+v", c.key, got)
		}
	}
}

func TestUserCancelsMonitorDuringSyntheticCheck(t *testing.T) {
	r := startNative(t, "echo")
	id, source := taskParent(t, r)
	now := r.service.now()
	r.service.now = func() time.Time { return now }
	cwd := t.TempDir()
	if _, err := r.service.SetProject(t.Context(), "bot", id, cwd); err != nil {
		t.Fatal(err)
	}
	args := engine.MonitorSetArgs{DelaySeconds: 60, MaxRuns: 1, Prompt: "wait for fixture", CheckCommand: `printf test > started; sleep 15; exit 10`}
	// A real shell with only a private synthetic marker; it waits for a bounded duration until cancelled.

	info, err := r.service.ToolMonitorSet(t.Context(), source, args)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.service.FireDueMonitors(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("monitor did not stop")
		}
	})
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(cwd, "started")); return err == nil })
	got, err := r.service.CancelMonitor(t.Context(), "bot", id, info.ID)
	if err != nil || got.State != "cancelled" {
		t.Fatalf("cancel: %+v, %v", got, err)
	}
	select {
	case err := <-done:
		done <- err
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not stop the check")
	}
	if _, err := r.q.GetSubmission(t.Context(), fmt.Sprintf("monitor:%s:1", info.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cancelled check enqueued a result: %v", err)
	}
}

func TestUserMonitorOfflineEnvironmentDoesNotRunLocally(t *testing.T) {
	r := startNative(t, "echo")
	id, source := taskParent(t, r)
	now := r.service.now()
	r.service.now = func() time.Time { return now }
	if _, err := r.q.CreateEnvironment(t.Context(), sqlc.CreateEnvironmentParams{ID: "offline", Name: "Disconnected fixture", Kind: "runner", Config: `{}`, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.conn.ExecContext(t.Context(), `UPDATE conversations SET environment_id = 'offline' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	info, err := r.service.ToolMonitorSet(t.Context(), source, engine.MonitorSetArgs{DelaySeconds: 60, Prompt: "check fixture", CheckCommand: `exit 10`})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := r.service.FireDueMonitors(t.Context()); err != nil {
		t.Fatal(err)
	}
	status := listedMonitor(t, r, id, info.ID)
	if status.State != "cancelled" || !strings.Contains(status.Outcome, "offline") {
		t.Fatalf("offline monitor fell back or did not report failure: %+v", status)
	}
}

func listedMonitor(t *testing.T, r *nativeTest, id int64, monitor string) engine.MonitorInfo {
	t.Helper()
	list, err := r.service.ListMonitors(t.Context(), "bot", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list {
		if item.ID == monitor {
			return item
		}
	}
	t.Fatalf("monitor %s not listed: %+v", monitor, list)
	return engine.MonitorInfo{}
}

func TestUserMonitorQueuesWithoutSteeringAndRechecksCancellation(t *testing.T) {
	r := startNative(t, "bash")
	id, source := taskParent(t, r)
	now := r.service.now()
	r.service.now = func() time.Time { return now }
	cwd := t.TempDir()
	if _, err := r.service.SetProject(t.Context(), "bot", id, cwd); err != nil {
		t.Fatal(err)
	}
	info, err := r.service.ToolMonitorSet(t.Context(), source, engine.MonitorSetArgs{DelaySeconds: 60, IntervalSeconds: 60, MaxRuns: 2, Prompt: "synthetic queued follow-up"})
	if err != nil {
		t.Fatal(err)
	}
	// Given Bash is blocked on a private fixture with a bounded loop.
	if _, err := r.service.Post(t.Context(), "bot", MainKey, "", "busy-run", `printf started > started; for i in {1..100}; do test -f release && break; sleep .05; done; printf finished`); err != nil {
		t.Fatal(err)
	}
	if err := r.service.admit(t.Context(), taskInput(t, r, "busy-run")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(cwd, "started")); return err == nil })
	// When the monitor fires and is admitted while the first turn remains active.
	now = now.Add(time.Minute)
	if err := r.service.FireDueMonitors(t.Context()); err != nil {
		t.Fatal(err)
	}
	event := "monitor:" + info.ID + ":1"
	if err := r.service.admit(t.Context(), taskInput(t, r, event)); err != nil {
		t.Fatal(err)
	}
	// The event is ordered as a separate native follow-up, not part of the first run.
	got, err := r.q.GetSubmission(t.Context(), event)
	if err != nil || !got.AdmittedAt.Valid || got.Status != "pending" || got.WhenBusy != "followUp" {
		t.Fatalf("not a native follow-up: %+v, %v", got, err)
	}
	if _, err := r.service.CancelMonitor(t.Context(), "bot", id, info.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "busy-run")
	awaitReceipt(t, r, event)
	got, err = r.q.GetSubmission(t.Context(), event)
	if err != nil || got.Status != "unanswered" || got.Answer.Valid {
		t.Fatalf("cancelled queued follow-up reached the model: %+v, %v", got, err)
	}
}
