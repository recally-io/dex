package app_test

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// holdAdmission makes every admission of the task with this title fail, so the task stays queued.
// Tasks have no capacity limit, so a test that needs a queued task holds one this way.
// The caller calls the returned function to release the hold; cleanup also releases it.
func holdAdmission(t *testing.T, dir, title string) func() {
	t.Helper()
	rw, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("hold_%p", rw)
	if _, err = rw.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE OF status ON tasks
WHEN OLD.title = '%s' AND NEW.status = 'admitting' BEGIN SELECT RAISE(FAIL, 'admission held by test'); END`, name, title)); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			// Cleanup runs after t.Context is cancelled.
			if _, err := rw.ExecContext(context.Background(), "DROP TRIGGER "+name); err != nil {
				t.Errorf("release admission hold: %v", err)
			}
			_ = rw.Close()
		})
	}
	t.Cleanup(release)
	return release
}

func TestUserNineModifyingTasksInOneDirectoryRunAtOnce(t *testing.T) {
	// Given main, whose tasks all use the agent workspace and modify files by default.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := d.mainChat(t)
	d.setModel(t, "spawn")
	names := make([]string, 9)
	// When main delegates nine gated tasks.
	for i := range names {
		names[i] = fmt.Sprintf("parallel-%d", i)
		taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": names[i], "goal": taskGate(t, dir, names[i]), "provider": "faux", "modelId": "bash"}))
	}
	// Then all nine run at the same time, before any of them is released.
	for _, name := range names {
		waitTaskFile(t, filepath.Join(dir, name+".pid"))
		waitExecution(t, conn, parent, name, "running")
	}
	for _, name := range names {
		releaseResultGate(t, dir, name)
	}
	for _, name := range names {
		waitExecution(t, conn, parent, name, "done")
	}
}

func TestUserQueuedTaskCanBeCancelledOverHTTPWithoutResult(t *testing.T) {
	t.Parallel()
	// Given a task that stays queued because its admission is held.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := d.mainChat(t)
	d.setModel(t, "spawn")
	holdAdmission(t, dir, "cancel-queued")
	spawn := taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "cancel-queued", "goal": "touch " + filepath.Join(dir, "cancelled"), "provider": "faux", "modelId": "echo"}))
	var task struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(spawn, "spawn: ")), &task); err != nil {
		t.Fatal(err)
	}
	waitExecution(t, conn, parent, "cancel-queued", "queued")
	// When the user cancels it over HTTP.
	status, _ := d.call(t, http.MethodPost, "/api/agents/default/tasks/"+task.ID+"/cancel", nil)
	if status != http.StatusOK {
		t.Fatalf("cancel status=%d", status)
	}
	// Then it is cancelled and creates no result.
	waitExecution(t, conn, parent, "cancel-queued", "cancelled")
	var results int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE id = ?", "task:"+task.ID+":result").Scan(&results); err != nil || results != 0 {
		t.Fatalf("cancelled task results=%d err=%v", results, err)
	}
}
