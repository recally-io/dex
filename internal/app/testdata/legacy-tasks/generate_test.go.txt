package app_test

import (
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// Run this file as an overlay test against dex commit 096a9c7 (the last commit with background
// tasks). It writes control.sqlite and agents/default/state.sqlite snapshots to
// DEX_LEGACY_FIXTURE_DIR. See README.md in this directory.
func TestGenerateLegacyTaskFixture(t *testing.T) {
	output := os.Getenv("DEX_LEGACY_FIXTURE_DIR")
	if output == "" {
		t.Fatal("DEX_LEGACY_FIXTURE_DIR is required")
	}
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	d.setModel(t, "spawn")
	// Task "Ended": finished, never continued. Its conversation still has the task-report edit.
	parent := d.newChat(t, taskJSON(t, map[string]any{"title": "Ended", "goal": "ended goal", "provider": "faux", "modelId": "echo"}))
	waitExecution(t, conn, parent, "Ended", "done")
	// Task "Continued": finished, then the user continued it.
	taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "Continued", "goal": "continued goal", "provider": "faux", "modelId": "echo"}))
	continued := waitExecution(t, conn, parent, "Continued", "done")
	conv := taskConversation(t, d, continued.ID)
	d.must(t, http.MethodPost, continueURL(conv), map[string]string{"id": "0194f2c0-0000-7000-8000-00000000c0de", "text": "thanks"}, nil, http.StatusAccepted)
	d.watch(t, conv).until(t, "continued answer", answeredWith("echo: thanks"))
	// Task "Running": still running when the snapshot is taken.
	taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "Running", "goal": taskGate(t, dir, "legacy"), "provider": "faux", "modelId": "bash"}))
	waitExecution(t, conn, parent, "Running", "running")
	waitTaskFile(t, filepath.Join(dir, "legacy.pid"))
	if err := os.MkdirAll(filepath.Join(output, "agents", "default"), 0o700); err != nil {
		t.Fatal(err)
	}
	for src, dst := range map[string]string{
		filepath.Join(dir, "control.sqlite"):                    filepath.Join(output, "control.sqlite"),
		filepath.Join(dir, "agents", "default", "state.sqlite"): filepath.Join(output, "agents", "default", "state.sqlite"),
	} {
		store, err := sql.Open("sqlite", "file:"+src+"?_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		_, snapshotErr := store.ExecContext(t.Context(), "VACUUM INTO ?", dst)
		if closeErr := store.Close(); snapshotErr != nil || closeErr != nil {
			t.Fatalf("snapshot %s: %v; close: %v", src, snapshotErr, closeErr)
		}
	}
	t.Logf("parent=%d continued=%s", parent, continued.ID)
}
