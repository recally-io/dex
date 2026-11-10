package app_test

import (
	"path/filepath"
	"testing"
)

func TestUserQueuedTaskStartsAfterRestart(t *testing.T) {
	t.Parallel()
	// Given a task that is still queued when dex stops.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := d.mainChat(t)
	d.setModel(t, "spawn")
	release := holdAdmission(t, dir, "restart-queued")
	taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "restart-queued", "goal": taskGate(t, dir, "restart-queued"), "provider": "faux", "modelId": "bash"}))
	waitExecution(t, conn, parent, "restart-queued", "queued")
	d.stop()
	release()
	// When dex starts again.
	startDex(t, dir, "")
	// Then the queued task starts and finishes.
	waitTaskFile(t, filepath.Join(dir, "restart-queued.pid"))
	waitExecution(t, conn, parent, "restart-queued", "running")
	releaseResultGate(t, dir, "restart-queued")
	waitExecution(t, conn, parent, "restart-queued", "done")
}
