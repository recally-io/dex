package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserReadsTaskStageToolsWhileRunning(t *testing.T) {
	// Given an actual shell command that waits for a file-controlled gate.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "spawn")
	parent := d.newChat(t, taskJSON(t, map[string]string{"title": "Transcript", "goal": taskGate(t, dir, "transcript"), "provider": "faux", "modelId": "bash"}))
	task := waitUserTask(t, d, parent, "Transcript", "running", 1)
	waitTaskFile(t, filepath.Join(dir, "transcript.pid"))
	path := fmt.Sprintf("/api/chats/%d/tasks/%s/stages/%s/events", parent, task.ID, task.Stages[0].ID)

	// When the user opens the stage during execution, the snapshot contains its running tool.
	w := d.watchPath(t, path)
	runningTool := func(tr *transcript) bool {
		for _, it := range tr.items {
			if it.Kind == "tool" && it.Name == "bash" && it.Status == "running" {
				return tr.running && tr.snapshots > 0
			}
		}
		return false
	}
	w.until(t, "running bash tool", runningTool)
	w.cancel()
	// A second connection reads a fresh snapshot while the same command still runs.
	w = d.watchPath(t, path)
	w.until(t, "reconnected running snapshot", runningTool)

	// Then unrelated parents, task IDs, and stages cannot read the transcript.
	d.setModel(t, "spawn")
	otherParent := d.newChat(t, taskJSON(t, map[string]string{"title": "Other", "goal": "other report", "provider": "faux", "modelId": "echo"}))
	other := waitUserTask(t, d, otherParent, "Other", "done", 1)
	for _, bad := range []string{
		fmt.Sprintf("/api/chats/%d/tasks/%s/stages/%s/events", otherParent, task.ID, task.Stages[0].ID),
		fmt.Sprintf("/api/chats/%d/tasks/%s/stages/%s/events", otherParent, task.ShortID, task.Stages[0].ID),
		fmt.Sprintf("/api/chats/%d/tasks/%s/stages/%s/events", parent, other.ID, task.Stages[0].ID),
		fmt.Sprintf("/api/chats/%d/tasks/%s/stages/%s/events", parent, task.ID, other.Stages[0].ID),
		fmt.Sprintf("/api/chats/%d/tasks/%s/stages/missing/events", parent, task.ID),
	} {
		d.must(t, http.MethodGet, bad, nil, nil, http.StatusNotFound)
	}
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var hidden int64
	if err := conn.QueryRowContext(t.Context(), "SELECT conversation_id FROM task_stages WHERE id = ?", task.Stages[0].ID).Scan(&hidden); err != nil {
		t.Fatal(err)
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/chats/%d/events", hidden), nil, nil, http.StatusNotFound)

	// The main chat remains writable while the stage is watched.
	setTaskChatModel(t, d, parent, "echo")
	if got := taskTurn(t, d, parent, "foreground available"); got != "echo: foreground available" {
		t.Fatal(got)
	}
	if err := os.WriteFile(filepath.Join(dir, "transcript.gate"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	complete := func(tr *transcript) bool {
		for _, it := range tr.items {
			if it.Kind == "tool" && it.Name == "bash" && it.Status == "done" && strings.Contains(it.Output, "gate released") {
				return !tr.running
			}
		}
		return false
	}
	w.until(t, "completed bash output", complete)
	w.cancel()
	// Short task IDs resolve only inside the original parent and replay the saved output.
	w = d.watchPath(t, fmt.Sprintf("/api/chats/%d/tasks/%s/stages/%s/events", parent, task.ShortID, task.Stages[0].ID))
	w.until(t, "saved tool snapshot", func(tr *transcript) bool { return tr.snapshots > 0 && complete(tr) })
}
