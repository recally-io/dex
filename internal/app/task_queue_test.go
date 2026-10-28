package app_test

import (
	"encoding/json/v2"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserSixBackgroundTasksQueueAndStartInFIFOOrder(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := d.mainChat(t)
	d.setModel(t, "spawn")
	for i := range 6 {
		name := "queue-" + string(rune('a'+i))
		taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": name, "goal": taskGate(t, dir, name), "provider": "faux", "modelId": "bash", "modifiesFiles": false}))
	}
	for i := range 4 {
		name := "queue-" + string(rune('a'+i))
		waitTaskFile(t, filepath.Join(dir, name+".pid"))
		waitExecution(t, conn, parent, name, "running")
	}
	for i := 4; i < 6; i++ {
		waitExecution(t, conn, parent, "queue-"+string(rune('a'+i)), "queued")
	}
	for i := range 4 {
		name := "queue-" + string(rune('a'+i))
		next := "queue-" + string(rune('a'+i+1))
		releaseResultGate(t, dir, name)
		waitExecution(t, conn, parent, name, "done")
		waitTaskFile(t, filepath.Join(dir, next+".pid"))
		waitExecution(t, conn, parent, next, "running")
	}
	for _, name := range []string{"queue-e", "queue-f"} {
		releaseResultGate(t, dir, name)
		waitExecution(t, conn, parent, name, "done")
	}
}

func TestUserSameCwdLocksModifyingTasksAndSharesReadOnlyTasks(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := d.mainChat(t)
	d.setModel(t, "spawn")
	taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "modify-one", "goal": taskGate(t, dir, "modify-one"), "provider": "faux", "modelId": "bash"}))
	waitTaskFile(t, filepath.Join(dir, "modify-one.pid"))
	second := taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "modify-two", "goal": taskGate(t, dir, "modify-two"), "provider": "faux", "modelId": "bash"}))
	if !strings.Contains(second, "spawn:") {
		t.Fatal(second)
	}
	waitExecution(t, conn, parent, "modify-two", "queued")
	releaseResultGate(t, dir, "modify-one")
	waitTaskFile(t, filepath.Join(dir, "modify-two.pid"))
	waitExecution(t, conn, parent, "modify-two", "running")
	releaseResultGate(t, dir, "modify-two")
	waitExecution(t, conn, parent, "modify-two", "done")

	d.setModel(t, "spawn")
	taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "read-one", "goal": taskGate(t, dir, "read-one"), "provider": "faux", "modelId": "bash", "modifiesFiles": false}))
	taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "read-two", "goal": taskGate(t, dir, "read-two"), "provider": "faux", "modelId": "bash", "modifiesFiles": false}))
	waitTaskFile(t, filepath.Join(dir, "read-one.pid"))
	waitTaskFile(t, filepath.Join(dir, "read-two.pid"))
	releaseResultGate(t, dir, "read-one")
	releaseResultGate(t, dir, "read-two")
}

func TestUserQueuedTaskCanBeCancelledOverHTTPWithoutResult(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := d.mainChat(t)
	d.setModel(t, "spawn")
	taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "cancel-holder", "goal": taskGate(t, dir, "cancel-holder"), "provider": "faux", "modelId": "bash"}))
	waitTaskFile(t, filepath.Join(dir, "cancel-holder.pid"))
	spawn := taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "cancel-queued", "goal": "touch " + filepath.Join(dir, "cancelled"), "provider": "faux", "modelId": "echo"}))
	var task struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(spawn, "spawn: ")), &task); err != nil {
		t.Fatal(err)
	}
	status, _ := d.call(t, http.MethodPost, "/api/agents/default/tasks/"+task.ID+"/cancel", nil)
	if status != http.StatusOK {
		t.Fatalf("cancel status=%d", status)
	}
	waitExecution(t, conn, parent, "cancel-queued", "cancelled")
	releaseResultGate(t, dir, "cancel-holder")
	waitExecution(t, conn, parent, "cancel-holder", "done")
}

func TestUserOrdinaryChatAnswersWhileTaskQueueWaits(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	parent := d.mainChat(t)
	d.setModel(t, "spawn")
	taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "busy", "goal": taskGate(t, dir, "busy"), "provider": "faux", "modelId": "bash"}))
	waitTaskFile(t, filepath.Join(dir, "busy.pid"))
	taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "waiting", "goal": taskGate(t, dir, "waiting"), "provider": "faux", "modelId": "bash"}))
	d.setModel(t, "echo")
	if got := taskTurn(t, d, parent, "ordinary input"); got != "echo: ordinary input" {
		t.Fatalf("ordinary chat: %s", got)
	}
	releaseResultGate(t, dir, "busy")
	releaseResultGate(t, dir, "waiting")
}
