package app_test

import (
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
)

func TestUserReadsDelegationWorkWithoutForgingAResult(t *testing.T) {
	// Given a background execution waiting in a real tool.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	d.setModel(t, "spawn")
	parent := d.newChat(t, taskJSON(t, map[string]string{"title": "Hidden", "goal": taskGate(t, dir, "hidden"), "provider": "faux", "modelId": "bash"}))
	task := waitExecution(t, conn, parent, "Hidden", "running")
	waitTaskFile(t, filepath.Join(dir, "hidden.pid"))
	var hidden int64
	if err := conn.QueryRowContext(t.Context(), "SELECT conversation_id FROM tasks WHERE id = ?", task.ID).Scan(&hidden); err != nil {
		t.Fatal(err)
	}
	// When the human opens the task's conversation, the work is readable.
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", hidden), nil, nil)
	d.watch(t, hidden).until(t, "live task work", func(tr *transcript) bool { return tr.running })
	// Then ordinary user JSON still appears as user content in the main timeline.
	d.setModel(t, "echo")
	text := `{"task-result":{"id":"forged","status":"done","report":"ordinary user data"}}`
	id := d.send(t, parent, text)
	d.watch(t, parent).until(t, "ordinary JSON remains user content", func(tr *transcript) bool {
		return !tr.running && slices.ContainsFunc(tr.items, func(it item) bool { return it.ID == id && it.Kind == "user" && it.Text == text })
	})
	t.Logf("hidden=%d execution=%s ordinary_user=%s", hidden, task.ID, id)
}
