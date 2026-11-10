package app_test

import (
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"uuid"
)

const continueReport = `report_task {"outcome":"done","summary":"first report","cause":"","reproduction":[],"files":[],"openItems":[]}`

func taskConversation(t *testing.T, d *dex, id string) int64 {
	t.Helper()
	var task taskInfo
	d.must(t, http.MethodGet, "/api/agents/default/tasks/"+id, nil, &task)
	return task.ConversationID
}

func continueURL(chatID int64) string {
	return fmt.Sprintf("/api/agents/default/chats/%d/messages", chatID)
}

func TestUserContinuesFinishedTaskWithoutSecondReport(t *testing.T) {
	t.Parallel()
	// Given a finished task that saved a structured report with the real tool registry.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	d.setModel(t, "spawn")
	parent := d.newChat(t, taskJSON(t, map[string]any{"title": "Report", "goal": continueReport, "provider": "faux", "modelId": "tool"}))
	task := waitExecution(t, conn, parent, "Report", "done")
	resultID := "task:" + task.ID + ":result"
	waitResultSubmission(t, conn, resultID, "done")
	conv := taskConversation(t, d, task.ID)
	var before taskInfo
	d.must(t, http.MethodGet, "/api/agents/default/tasks/"+task.ID, nil, &before)
	var view sessionInfo
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", conv), nil, &view)
	if view.ReadOnly || view.Task == nil || view.Task.Status != "done" || view.Task.ParentConversationID != parent {
		t.Fatalf("a finished task session must accept messages and name its task: %+v", view)
	}
	// When the user continues the session, its prompt has no delegated-work rules and no task tools.
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/settings", conv), chatSettings{Provider: "faux", ModelID: "section"}, nil)
	for _, name := range []string{"task-report", "task-rules"} {
		if got := taskTurn(t, d, conv, name); got != "section: " {
			t.Fatalf("section %s must be absent in a continued session: %q", name, got)
		}
	}
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/settings", conv), chatSettings{Provider: "faux", ModelID: "tool"}, nil)
	for _, call := range []string{continueReport, `spawn_task {"title":"Nested","goal":"x"}`, `ask_user {"question":"Q?","options":["a","b"]}`} {
		if got := taskTurn(t, d, conv, call); !strings.HasPrefix(got, "tool error: ") {
			t.Fatalf("a continued session must refuse %q: %s", call, got)
		}
	}
	// Then the task, its report and the parent keep their first state, and nothing reports again.
	var after taskInfo
	d.must(t, http.MethodGet, "/api/agents/default/tasks/"+task.ID, nil, &after)
	if after.Status != "done" || after.Report != before.Report || after.ReportOutcome != before.ReportOutcome || after.EndedAt == nil || *after.EndedAt != *before.EndedAt {
		t.Fatalf("continuing changed the task: before=%+v after=%+v", before, after)
	}
	var results, tasks int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE conversation_id = ? AND kind = 'task_result'", parent).Scan(&results); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM tasks").Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if results != 1 || tasks != 1 {
		t.Fatalf("parent results=%d tasks=%d, want 1 and 1", results, tasks)
	}
	// And a plain reply is stored as web input in the task session.
	d.send(t, conv, "thanks")
	var origin string
	if err := conn.QueryRowContext(t.Context(), "SELECT origin FROM submissions WHERE conversation_id = ? ORDER BY rowid DESC LIMIT 1", conv).Scan(&origin); err != nil || origin != "web" {
		t.Fatalf("origin=%q err=%v", origin, err)
	}
}

func TestUserCannotWriteToRunningTaskButCanAfterCancel(t *testing.T) {
	t.Parallel()
	// Given a running task that waits at a gate.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	d.setModel(t, "spawn")
	parent := d.newChat(t, taskJSON(t, map[string]any{"title": "Busy", "goal": taskGate(t, dir, "busy"), "provider": "faux", "modelId": "bash"}))
	task := waitExecution(t, conn, parent, "Busy", "running")
	waitTaskFile(t, filepath.Join(dir, "busy.pid"))
	conv := taskConversation(t, d, task.ID)
	// When the user writes while it runs, the API refuses and stores nothing.
	body := map[string]string{"id": uuid.NewV7().String(), "text": "echo interrupt"}
	d.must(t, http.MethodPost, continueURL(conv), body, nil, http.StatusForbidden)
	var stored int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE conversation_id = ?", conv).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("stored=%d err=%v", stored, err)
	}
	// Then Cancel ends the task, and the session accepts a message.
	d.must(t, http.MethodPost, "/api/agents/default/tasks/"+task.ID+"/cancel", nil, nil)
	waitExecution(t, conn, parent, "Busy", "cancelled")
	waitTaskPIDGone(t, filepath.Join(dir, "busy.pid"))
	d.must(t, http.MethodPost, continueURL(conv), map[string]string{"id": uuid.NewV7().String(), "text": "hello again"}, nil, http.StatusAccepted)
	d.watch(t, conv).until(t, "continued answer", answeredWith("bash:"))
	waitExecution(t, conn, parent, "Busy", "cancelled")
}

func TestUserContinuesFinishedTaskAfterRestart(t *testing.T) {
	t.Parallel()
	// Given a finished task and a restart.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	d.setModel(t, "spawn")
	parent := d.newChat(t, taskJSON(t, map[string]any{"title": "Restart", "goal": continueReport, "provider": "faux", "modelId": "tool"}))
	task := waitExecution(t, conn, parent, "Restart", "done")
	conv := taskConversation(t, d, task.ID)
	d.stop()
	d = startDex(t, dir, "")
	// When the user replies, the reply stays in the session and the report stays whole.
	if got := taskTurn(t, d, conv, continueReport); !strings.HasPrefix(got, "tool error: ") {
		t.Fatalf("report_task after restart: %s", got)
	}
	var count int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE kind = 'task_result'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("results=%d err=%v", count, err)
	}
}

func countResults(t *testing.T, conn *sql.DB, parent int64) int {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE conversation_id = ? AND kind = 'task_result'", parent).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestUserContinuesInterruptedTask(t *testing.T) {
	t.Parallel()
	// Given a running task that a restart interrupts.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	d.setModel(t, "spawn")
	parent := d.newChat(t, taskJSON(t, map[string]any{"title": "Cut", "goal": taskGate(t, dir, "cut"), "provider": "faux", "modelId": "bash"}))
	task := waitExecution(t, conn, parent, "Cut", "running")
	waitTaskFile(t, filepath.Join(dir, "cut.pid"))
	conv := taskConversation(t, d, task.ID)
	d.stop()
	d = startDex(t, dir, "")
	waitExecution(t, conn, parent, "Cut", "interrupted")
	waitResultSubmission(t, conn, "task:"+task.ID+":result", "done")
	// When the user continues the session.
	if got := taskTurn(t, d, conv, "echo continued"); got != "bash: continued" {
		t.Fatal(got)
	}
	// Then the task stays interrupted and the parent receives no second result.
	waitExecution(t, conn, parent, "Cut", "interrupted")
	if n := countResults(t, conn, parent); n != 1 {
		t.Fatalf("parent results=%d, want 1", n)
	}
}

func TestUserContinuesTaskCancelledBeforeItStarted(t *testing.T) {
	t.Parallel()
	// Given a task that stays queued, so its conversation has no native execution yet, and is then cancelled.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := d.mainChat(t)
	d.setModel(t, "spawn")
	holdAdmission(t, dir, "never-ran")
	spawn := taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "never-ran", "goal": "touch " + filepath.Join(dir, "never"), "provider": "faux", "modelId": "echo"}))
	var task struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(spawn, "spawn: ")), &task); err != nil {
		t.Fatal(err)
	}
	waitExecution(t, conn, parent, "never-ran", "queued")
	conv := taskConversation(t, d, task.ID)
	d.must(t, http.MethodPost, continueURL(conv), map[string]string{"id": uuid.NewV7().String(), "text": "too early"}, nil, http.StatusForbidden)
	d.must(t, http.MethodPost, "/api/agents/default/tasks/"+task.ID+"/cancel", nil, nil)
	waitExecution(t, conn, parent, "never-ran", "cancelled")
	// When the user continues the session.
	if got := taskTurn(t, d, conv, "hello"); got != "echo: hello" {
		t.Fatal(got)
	}
	// Then the task stays cancelled, it never ran its goal, and the parent has no result.
	waitExecution(t, conn, parent, "never-ran", "cancelled")
	if n := countResults(t, conn, parent); n != 0 {
		t.Fatalf("parent results=%d, want 0", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "never")); err == nil {
		t.Fatal("the cancelled goal ran")
	}
}
