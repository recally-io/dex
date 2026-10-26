package app_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/engine"
)

func maintenanceEngine(t *testing.T, data string) *engine.Agents {
	t.Helper()
	agents, err := engine.New(engine.Config{DataDir: data, Home: t.TempDir(), BaseEnv: map[string]string{"PATH": os.Getenv("PATH")}, FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := agents.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return agents
}

func maintenanceConversation(t *testing.T, agents *engine.Agents) engine.Ref {
	t.Helper()
	ref, err := agents.Create(t.Context(), "default", engine.Options{Model: engine.ModelRef{Provider: "faux", ModelID: "bash"}, ThinkingLevel: "off"})
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func maintenanceSubmit(t *testing.T, agents *engine.Agents, ref engine.Ref, command string) *engine.Submission {
	t.Helper()
	submission, err := agents.Submit(t.Context(), ref, engine.Input{Model: engine.ModelRef{Provider: "faux", ModelID: "bash"}, ThinkingLevel: "off", RequestID: uuid.NewV7().String(), Content: command, Origin: "web", Source: "dex:default/chat/test"})
	if err != nil {
		t.Fatal(err)
	}
	return submission
}

func markerExists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestUserMaintenanceBlocksTranscriptRecovery(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"watch", "history"} {
		t.Run(method, func(t *testing.T) {
			// Given a stored run with a queued input that must survive an engine close.
			data, dir := t.TempDir(), t.TempDir()
			started, release := filepath.Join(dir, "started"), filepath.Join(dir, "release")
			defer func() { _ = os.WriteFile(release, nil, 0o600) }()
			first := maintenanceEngine(t, data)
			ref := maintenanceConversation(t, first)
			command := fmt.Sprintf("touch '%s'; while [ ! -f '%s' ]; do sleep 0.02; done; echo done", started, release)
			maintenanceSubmit(t, first, ref, command)
			eventually(t, "initial tool", func() (bool, any) { return markerExists(started), started })
			maintenanceSubmit(t, first, ref, command)
			if err := first.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(started); err != nil {
				t.Fatal(err)
			}
			second := maintenanceEngine(t, data)
			if err := second.Quiesce(t.Context()); err != nil {
				t.Fatal(err)
			}

			// When maintenance owns admission, reads cannot open and resume the stored run.
			read := func() error {
				if method == "history" {
					_, err := second.History(t.Context(), ref, 1, 1)
					return err
				}
				watch, err := second.Watch(t.Context(), ref)
				if err == nil {
					watch.Close()
				}
				return err
			}
			if err := read(); !errors.Is(err, engine.ErrMaintenance) {
				t.Fatalf("maintenance read: %v", err)
			}
			if markerExists(started) {
				t.Fatal("read resumed execution during maintenance")
			}

			// Then releasing maintenance permits recovery and the recovered run blocks a new lease.
			second.Resume()
			if err := read(); err != nil {
				t.Fatal(err)
			}
			eventually(t, "recovered tool", func() (bool, any) { return markerExists(started), started })
			if err := second.Quiesce(t.Context()); !errors.Is(err, engine.ErrBusy) {
				t.Fatalf("maintenance accepted active execution: %v", err)
			}
			if err := os.WriteFile(release, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUserMaintenanceWaitsForMemoryCommit(t *testing.T) {
	t.Parallel()
	// Given a native execution whose memory commit waits for a repository hook.
	data := t.TempDir()
	agents := maintenanceEngine(t, data)
	ref := maintenanceConversation(t, agents)
	dir := t.TempDir()
	started, release := filepath.Join(dir, "commit-started"), filepath.Join(dir, "release")
	defer func() { _ = os.WriteFile(release, nil, 0o600) }()
	script := filepath.Join(data, "memory", ".git", "commit-filter")
	filter := fmt.Sprintf("#!/bin/sh\ntouch '%s'\nwhile [ ! -f '%s' ]; do sleep 0.02; done\ncat\n", started, release)
	if err := os.WriteFile(script, []byte(filter), 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.CommandContext(t.Context(), "git", "-C", filepath.Join(data, "memory"), "config", "filter.gate.clean", script).CombinedOutput(); err != nil {
		t.Fatalf("configure commit filter: %s: %v", output, err)
	}
	if err := os.WriteFile(filepath.Join(data, "memory", ".gitattributes"), []byte("MEMORY.md filter=gate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	submission := maintenanceSubmit(t, agents, ref, fmt.Sprintf("echo change >> '%s'; echo done", filepath.Join(data, "memory", "MEMORY.md")))
	eventually(t, "memory commit started", func() (bool, any) { return markerExists(started), started })

	// When an update requests maintenance, the pending commit keeps it busy.
	if err := agents.Quiesce(t.Context()); !errors.Is(err, engine.ErrBusy) {
		t.Fatalf("maintenance accepted a pending memory commit: %v", err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := submission.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Then maintenance succeeds once both execution and the memory commit finish.
	eventually(t, "memory commit finishes", func() (bool, any) { err := agents.Quiesce(t.Context()); return err == nil, err })
	agents.Resume()
}

func TestUserCancelledWatchDoesNotStartAfterMaintenance(t *testing.T) {
	t.Parallel()
	// Given a native runtime paused before a browser subscribes.
	agents := maintenanceEngine(t, t.TempDir())
	ref := maintenanceConversation(t, agents)
	if err := agents.Quiesce(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	// When that browser has already disconnected, it receives no subscription handle.
	watch, err := agents.Watch(ctx, ref)
	if err == nil || watch != nil {
		if watch != nil {
			watch.Close()
		}
		t.Fatalf("cancelled watch: %v", err)
	}
	agents.Resume()
}
