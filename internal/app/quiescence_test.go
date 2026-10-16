package app_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/worker"
	"github.com/recally-io/dex/internal/workerbin"
)

// startMaintenanceWorker uses the real protocol without control replay, as after a
// restart when a browser opens a persisted conversation before any new submission.
func startMaintenanceWorker(t *testing.T, data string, settled func(context.Context, worker.Settled), chats ...func(worker.ChatStream)) (*worker.Worker, *chat.Service, func()) {
	t.Helper()
	cache := t.TempDir()
	path, err := worker.Install(cache, workerbin.Gzip)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	w := worker.New(worker.Config{Path: path, Dir: cache, Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "DEX_FAKE_MODEL=1", "DEX_DATA_DIR=" + data}, OnSettled: settled, OnChat: func(ev worker.ChatStream) {
		for _, f := range chats {
			f(ev)
		}
	}})
	done := make(chan struct{})
	go func() { _ = w.Run(ctx); close(done) }()
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	callCtx, callCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer callCancel()
	if err := w.Call(callCtx, "ping", struct{}{}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	return w, chat.New(nil, w, time.Now, nil, nil), stop
}
func workerConversation(t *testing.T, w *worker.Worker) int64 {
	t.Helper()
	var result worker.CreateConversationResult
	if err := w.Call(t.Context(), "createConversation", worker.CreateConversationParams{AgentID: "default", Model: worker.ModelRef{Provider: "faux", ModelID: "bash"}, ThinkingLevel: "off"}, &result); err != nil {
		t.Fatal(err)
	}
	return result.ConversationID
}
func workerSubmit(t *testing.T, w *worker.Worker, id int64, command string) {
	t.Helper()
	if err := w.Call(t.Context(), "submit", worker.SubmitParams{AgentID: "default", ConversationID: id, Model: worker.ModelRef{Provider: "faux", ModelID: "bash"}, ThinkingLevel: "off", RequestID: uuid.NewV7().String(), Content: command, Attachments: []worker.Attachment{}, Source: "dex:chat/test"}, &worker.SubmitResult{}); err != nil {
		t.Fatal(err)
	}
}
func markerExists(path string) bool { _, err := os.Stat(path); return err == nil }
func TestUserMaintenanceBlocksTranscriptRecovery(t *testing.T) {
	for _, method := range []string{"watch", "history", "inputEntries"} {
		for _, gated := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/gated=%t", method, gated), func(t *testing.T) {
				// Given a persisted bash run and a new control/worker pair without dispatch reservations.
				data := t.TempDir()
				dir := t.TempDir()
				started := filepath.Join(dir, "started")
				release := filepath.Join(dir, "release")
				defer func() { _ = os.WriteFile(release, nil, 0600) }()
				w, _, stop := startMaintenanceWorker(t, data, nil)
				id := workerConversation(t, w)
				workerPID := filepath.Join(dir, "worker-pid")
				shellPID := filepath.Join(dir, "shell-pid")
				workerSubmit(t, w, id, fmt.Sprintf("echo $PPID > '%s'; echo $$ > '%s'; touch '%s'; while [ ! -f '%s' ]; do sleep 0.02; done; echo done", workerPID, shellPID, started, release))
				eventually(t, "initial tool", func() (bool, any) { return markerExists(started), started })
				// The interrupted unsafe tool is not replayed. Its persisted next input must
				// still execute bash when a read resumes the harness.
				workerSubmit(t, w, id, fmt.Sprintf("touch '%s'; while [ ! -f '%s' ]; do sleep 0.02; done; echo recovered", started, release))
				// Kill the worker, rather than requesting a clean close that aborts its run.
				for _, file := range []string{workerPID, shellPID} {
					b, err := os.ReadFile(file)
					if err != nil {
						t.Fatal(err)
					}
					pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
					if err != nil {
						t.Fatal(err)
					}
					process, err := os.FindProcess(pid)
					if err != nil {
						t.Fatal(err)
					}
					_ = process.Kill()
				}
				stop()
				if err := os.Remove(started); err != nil {
					t.Fatal(err)
				}
				w, gate, _ := startMaintenanceWorker(t, data, nil)
				if gated {
					if err := gate.AcquireMaintenance(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				// When a transcript read opens the harness, maintenance must exclude resumed execution.
				read := make(chan error, 1)
				go func() {
					params := map[string]any{
						"watch":        worker.WatchParams{AgentID: "default", ConversationID: id, WatchID: uuid.NewV7().String()},
						"history":      worker.HistoryParams{AgentID: "default", ConversationID: id, Before: 1, Limit: 1},
						"inputEntries": worker.InputEntriesParams{AgentID: "default", ConversationID: id, RequestIDs: []string{}},
					}[method]
					read <- w.Call(t.Context(), method, params, &struct{}{})
				}()
				if gated {
					deadline := time.Now().Add(2 * time.Second)
					for time.Now().Before(deadline) {
						if markerExists(started) {
							t.Fatalf("%s resumed a tool while maintenance was held", method)
						}
						time.Sleep(time.Millisecond)
					}
					if err := gate.ReleaseMaintenance(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				if err := <-read; err != nil {
					t.Fatal(err)
				}
				eventually(t, "recovered tool", func() (bool, any) { return markerExists(started), started })
				// Then an already recovered run also prevents acquire, despite no local active submission.
				if err := gate.AcquireMaintenance(t.Context()); err == nil {
					t.Fatalf("maintenance accepted a run resumed by %s", method)
				}
				if err := os.WriteFile(release, nil, 0600); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
func TestUserMaintenanceWaitsForMemoryCommit(t *testing.T) {
	// Given a real git commit blocked by a repository hook after the settled event.
	data := t.TempDir()
	settled := make(chan struct{}, 1)
	w, gate, _ := startMaintenanceWorker(t, data, func(context.Context, worker.Settled) { settled <- struct{}{} })
	id := workerConversation(t, w)
	started := filepath.Join(t.TempDir(), "commit-started")
	release := filepath.Join(t.TempDir(), "release")
	defer func() { _ = os.WriteFile(release, nil, 0600) }()
	hook := fmt.Sprintf("#!/bin/sh\ntouch '%s'\nwhile [ ! -f '%s' ]; do sleep 0.02; done\n", started, release)
	if err := os.WriteFile(filepath.Join(data, "memory", ".git", "hooks", "pre-commit"), []byte(hook), 0755); err != nil {
		t.Fatal(err)
	}
	workerSubmit(t, w, id, fmt.Sprintf("echo change >> '%s'; echo done", filepath.Join(data, "memory", "MEMORY.md")))
	<-settled
	eventually(t, "memory commit started", func() (bool, any) { return markerExists(started), started })
	// When an updater acquires just after settle, pending memory work must make it busy.
	if err := gate.AcquireMaintenance(t.Context()); err == nil {
		t.Fatal("maintenance accepted a pending memory commit")
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	eventually(t, "memory commit finishes", func() (bool, any) { err := gate.AcquireMaintenance(t.Context()); return err == nil, err })
	if err := gate.ReleaseMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestUserCancelledWatchDoesNotStartAfterMaintenance(t *testing.T) {
	// Given a real worker held in maintenance before a browser opens its watch.
	events := make(chan worker.ChatStream, 16)
	w, gate, _ := startMaintenanceWorker(t, t.TempDir(), nil, func(ev worker.ChatStream) { events <- ev })
	id := workerConversation(t, w)
	if err := gate.AcquireMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	watchID := uuid.NewV7().String()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_ = w.Call(ctx, "watch", worker.WatchParams{AgentID: "default", ConversationID: id, WatchID: watchID}, &struct{}{})
	// When the browser cancels, unwatch must cancel pending admission too.
	if err := w.Call(t.Context(), "unwatch", worker.UnwatchParams{WatchID: watchID}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := gate.ReleaseMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	select {
	case ev := <-events:
		t.Fatalf("cancelled watch started after release: %+v", ev)
	case <-deadline.C:
	}
}
