package app_test

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"uuid"
)

func TestUserHandoffDoesNotCancelBackgroundTask(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "spawn")
	db := taskResultDB(t, dir)
	parent := d.mainChat(t)
	input := d.send(t, parent, taskJSON(t, map[string]string{"title": "Long work", "goal": taskGate(t, dir, "handoff-task"), "provider": "faux", "modelId": "bash"}))
	waitResultSubmission(t, db, input, "done")
	task := waitExecution(t, db, parent, "Long work", "running")
	waitTaskFile(t, filepath.Join(dir, "handoff-task.pid"))
	reset := uuid.NewV7().String()
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/clear", parent), map[string]string{"id": reset, "handoff": "continue later"}, nil, http.StatusAccepted)
	waitResultSubmission(t, db, reset, "done")
	var current taskInfo
	d.must(t, http.MethodGet, "/api/agents/default/tasks/"+task.ID, nil, &current)
	if current.Status != "running" {
		t.Fatalf("clear cancelled task: %+v", current)
	}
	releaseResultGate(t, dir, "handoff-task")
	waitExecution(t, db, parent, "Long work", "done")
	t.Logf("task %s continued across reset %s", task.ID, reset)
}
