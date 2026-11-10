package app_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestUserQueuedTaskNoModelFailsWithOneParentResult(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	parent := d.mainChat(t)
	d.setModel(t, "spawn")
	// Given a queued task whose model settings are cleared before admission.
	release := holdAdmission(t, dir, "model-cleared")
	taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "model-cleared", "goal": "must fail", "provider": "faux", "modelId": "echo"}))
	var conversation int64
	if err := conn.QueryRowContext(t.Context(), "SELECT conversation_id FROM tasks WHERE title = ?", "model-cleared").Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), "UPDATE conversations SET model_provider = '', model_id = '' WHERE id = ?", conversation); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), "UPDATE agents SET model_provider = '', model_id = '' WHERE id = 'default'"); err != nil {
		t.Fatal(err)
	}
	// When the task is admitted, then it fails with one parent result.
	release()
	task := waitExecution(t, conn, parent, "model-cleared", "failed")
	var results int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE source_key = ?", "task:"+task.ID+":result").Scan(&results); err != nil || results != 1 {
		t.Fatalf("parent results=%d err=%v", results, err)
	}
}
