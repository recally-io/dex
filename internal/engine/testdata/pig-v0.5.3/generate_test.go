package engine

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Run this file as an overlay test against dex commit 4a93b06, which pins PiG v0.5.3.
func TestGeneratePiG053UpgradeFixture(t *testing.T) {
	output := os.Getenv("DEX_PIG_FIXTURE_DIR")
	if output == "" {
		t.Fatal("DEX_PIG_FIXTURE_DIR is required")
	}
	a := coreEngine(t, Config{})
	chat := coreCreate(t, a, "upgrade", coreOptions("echo"))
	for _, message := range []struct{ id, text, model string }{
		{"before-reset", "before reset", "echo"},
		{"after-reset", "after reset", "echo"},
		{"legacy-tool", "printf legacy-tool", "bash"},
	} {
		if message.id == "after-reset" {
			if err := a.Reset(t.Context(), chat, "legacy-reset"); err != nil {
				t.Fatal(err)
			}
		}
		if got := coreWait(t, coreSubmit(t, a, chat, message.id, message.text, message.model)); got.Status != "done" {
			t.Fatalf("fixture input %s: %+v", message.id, got)
		}
	}
	task := coreCreate(t, a, "upgrade", coreOptions("echo"))
	sub, err := a.Submit(t.Context(), task, Input{
		RequestID: "legacy-task-input", Content: "legacy task", Kind: "message", Origin: "task",
		Source: "dex:upgrade/chat/2", TaskID: "legacy-task", TaskTitle: "Legacy task",
		Background: true, Options: coreOptions("echo"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := coreWait(t, sub); got.Status != "done" {
		t.Fatalf("fixture task: %+v", got)
	}
	if sealed, err := a.SealTask(t.Context(), task, "legacy-task-input"); err != nil || !sealed {
		t.Fatalf("seal fixture task: %v, %v", sealed, err)
	}
	running := coreCreate(t, a, "upgrade", coreOptions("bash"))
	marker := filepath.Join(a.config.DataDir, "tool-started")
	_, err = a.Submit(t.Context(), running, Input{
		RequestID: "legacy-running-input", Background: true, Options: coreOptions("bash"),
		Content: fmt.Sprintf("touch upgrade-replayed-task; printf ready > %q; sleep 3600", marker),
	})
	if err != nil {
		t.Fatal(err)
	}
	coreFile(t, marker)
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	// VACUUM captures a consistent store while the old tool is still running.
	store, err := sql.Open("sqlite", filepath.Join(a.config.DataDir, "agents", "upgrade", "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, snapshotErr := store.ExecContext(t.Context(), "VACUUM INTO ?", filepath.Join(output, "state.sqlite"))
	closeErr := store.Close()
	if snapshotErr != nil || closeErr != nil {
		t.Fatalf("snapshot: %v; close: %v", snapshotErr, closeErr)
	}
	if err := a.Abort(t.Context(), running); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Logf("generated chat=%d task=%d running=%d in %s", chat.ID, task.ID, running.ID, output)
}
