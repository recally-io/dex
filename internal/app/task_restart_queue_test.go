package app_test

import (
	"path/filepath"
	"testing"
)

func TestUserQueuedTaskOrderSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := d.mainChat(t)
	d.setModel(t, "spawn")
	taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "restart-holder", "goal": taskGate(t, dir, "restart-holder"), "provider": "faux", "modelId": "bash"}))
	waitTaskFile(t, filepath.Join(dir, "restart-holder.pid"))
	taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "restart-queued", "goal": taskGate(t, dir, "restart-queued"), "provider": "faux", "modelId": "bash"}))
	waitExecution(t, conn, parent, "restart-queued", "queued")
	d.stop()
	next := startDex(t, dir, "")
	waitExecution(t, conn, parent, "restart-holder", "interrupted")
	waitTaskFile(t, filepath.Join(dir, "restart-queued.pid"))
	waitExecution(t, conn, parent, "restart-queued", "running")
	releaseResultGate(t, dir, "restart-queued")
	waitExecution(t, conn, parent, "restart-queued", "done")
	_ = next
}
