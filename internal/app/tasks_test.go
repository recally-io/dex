package app_test

import (
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/recally-io/dex/internal/chat"
)

func TestUserAnswersWhileDelegationRunsAndReceivesItsReport(t *testing.T) {
	// Given a background execution with a real artifact and a file gate.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	d.setModel(t, "spawn")
	parent := d.newChat(t, taskJSON(t, map[string]string{"title": "Artifact", "goal": taskGate(t, dir, "artifact"), "provider": "faux", "modelId": "bash"}))
	task := waitExecution(t, conn, parent, "Artifact", "running")
	waitTaskFile(t, filepath.Join(dir, "artifact.pid"))
	// When the user sends an ordinary question while the background shell waits.
	setTaskChatModel(t, d, parent, "echo")
	if got := taskTurn(t, d, parent, "foreground available"); got != "echo: foreground available" {
		t.Fatal(got)
	}
	if got := waitTaskFile(t, filepath.Join(dir, "artifact.output")); got != "real artifact\n" {
		t.Fatal(got)
	}
	// Then the result returns to the original conversation and its report remains queryable.
	setTaskChatModel(t, d, parent, "spawn")
	releaseResultGate(t, dir, "artifact")
	d.watch(t, parent).until(t, "source result", answeredWith(`"id":"`+task.ID+`"`))
	task = waitExecution(t, conn, parent, "Artifact", "done")
	if !strings.Contains(taskTool(t, d, parent, "get_task", map[string]string{"id": task.ID}), "gate released") {
		t.Fatal("get_task lost report")
	}
	if !strings.Contains(taskTool(t, d, parent, "list_tasks", map[string]string{}), task.ID) {
		t.Fatal("list_tasks omitted execution")
	}
	if chats := d.chats(t); len(chats) != 1 || chats[0].ID != parent {
		t.Fatalf("hidden chat leaked: %+v", chats)
	}
	t.Logf("execution=%s report=%q", task.ID, task.Report)
}

func TestUserDelegationSlotsAndToolsRespectConversationOwnership(t *testing.T) {
	// Given two chats on one agent, each holding one background execution.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	d.setModel(t, "spawn")
	firstChat := d.newChat(t, taskJSON(t, map[string]string{"title": "First", "goal": taskGate(t, dir, "first"), "provider": "faux", "modelId": "bash"}))
	first := waitExecution(t, conn, firstChat, "First", "running")
	waitTaskFile(t, filepath.Join(dir, "first.pid"))
	secondChat := d.newChat(t, taskJSON(t, map[string]string{"title": "Second", "goal": taskGate(t, dir, "second"), "provider": "faux", "modelId": "bash"}))
	second := waitExecution(t, conn, secondChat, "Second", "running")
	waitTaskFile(t, filepath.Join(dir, "second.pid"))
	// When another dispatch is attempted, the agent-wide limit rejects it.
	if got := spawnExecution(t, d, firstChat, "Third", "must not run", "echo"); !strings.Contains(got, "error") {
		t.Fatal(got)
	}
	// Then another conversation cannot inspect or cancel the first execution.
	for _, tool := range []string{"get_task", "cancel_task"} {
		if got := taskTool(t, d, secondChat, tool, map[string]string{"id": first.ID}); !strings.Contains(got, "not found") {
			t.Fatalf("%s: %s", tool, got)
		}
	}
	if got := taskTool(t, d, secondChat, "list_tasks", map[string]string{}); strings.Contains(got, first.ID) || !strings.Contains(got, second.ID) {
		t.Fatal(got)
	}
	taskTool(t, d, firstChat, "cancel_task", map[string]string{"id": first.ID})
	waitExecution(t, conn, firstChat, "First", "cancelled")
	waitTaskPIDGone(t, filepath.Join(dir, "first.pid"))
	spawnExecution(t, d, firstChat, "Reused slot", "bounded report", "echo")
	waitExecution(t, conn, firstChat, "Reused slot", "done")
	taskTool(t, d, secondChat, "cancel_task", map[string]string{"id": second.ID})
	waitExecution(t, conn, secondChat, "Second", "cancelled")
}

func TestUserRestartDoesNotRerunStartedDelegationOrKeepItsSlot(t *testing.T) {
	// Given a shell that records every start before waiting at a gate.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	d.setModel(t, "spawn")
	goal := "printf 'run\\n' >> " + resultShellQuote(filepath.Join(dir, "counter")) + "; " + taskGate(t, dir, "restart")
	parent := d.newChat(t, taskJSON(t, map[string]string{"title": "Interrupted", "goal": goal, "provider": "faux", "modelId": "bash"}))
	task := waitExecution(t, conn, parent, "Interrupted", "running")
	waitTaskFile(t, filepath.Join(dir, "restart.pid"))
	// When control and the real worker restart against the same database.
	d.stop()
	d = startDex(t, dir, "")
	waitExecution(t, conn, parent, "Interrupted", "interrupted")
	// Then new executions can use both slots, without rerunning the interrupted shell.
	for _, name := range []string{"new-first", "new-second"} {
		spawnExecution(t, d, parent, name, taskGate(t, dir, name), "bash")
		waitExecution(t, conn, parent, name, "running")
	}
	if got := waitTaskFile(t, filepath.Join(dir, "counter")); got != "run\n" {
		t.Fatalf("reran: %q", got)
	}
	var results int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE source_key = ?", "task:"+task.ID+":result").Scan(&results); err != nil {
		t.Fatal(err)
	}
	if results != 0 {
		t.Fatalf("interrupted execution created %d results", results)
	}
	t.Logf("interrupted=%s counter=one new_slots=2", task.ID)
}

func spawnExecution(t *testing.T, d *dex, parent int64, title, goal, model string) string {
	t.Helper()
	setTaskChatModel(t, d, parent, "spawn")
	return taskTurn(t, d, parent, taskJSON(t, map[string]string{"title": title, "goal": goal, "provider": "faux", "modelId": model}))
}

func waitExecution(t *testing.T, conn *sql.DB, parent int64, title, status string) chat.Task {
	t.Helper()
	var task chat.Task
	eventually(t, title+" "+status, func() (bool, any) {
		err := conn.QueryRowContext(t.Context(), "SELECT id,title,status,report,stop_note FROM tasks WHERE parent_conversation_id = ? AND title = ?", parent, title).Scan(&task.ID, &task.Title, &task.Status, &task.Report, &task.StopNote)
		return err == nil && task.Status == status, fmt.Sprintf("task=%+v err=%v", task, err)
	})
	return task
}

func taskJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func setTaskChatModel(t *testing.T, d *dex, chatID int64, model string) {
	t.Helper()
	d.must(t, http.MethodPut, fmt.Sprintf("/api/chats/%d/settings", chatID), map[string]string{"provider": "faux", "modelId": model, "thinkingLevel": "off"}, nil)
}

func taskTurn(t *testing.T, d *dex, chatID int64, text string) string {
	t.Helper()
	id := d.send(t, chatID, text)
	w := d.watch(t, chatID)
	w.until(t, "answer to "+id, func(tr *transcript) bool {
		for i, entry := range tr.items {
			if entry.ID == id && entry.Kind == "user" {
				return !tr.running && len(tr.items) > i+1 && tr.items[len(tr.items)-1].Kind == "assistant"
			}
		}
		return false
	})
	return w.tr.items[len(w.tr.items)-1].Text
}

func taskTool(t *testing.T, d *dex, chatID int64, name string, args any) string {
	t.Helper()
	setTaskChatModel(t, d, chatID, "tool")
	return taskTurn(t, d, chatID, name+" "+taskJSON(t, args))
}

func taskGate(t *testing.T, dir, name string) string {
	t.Helper()
	base := filepath.Join(dir, name)
	// The shell polls a gate until its deadline; cleanup can always release it.
	t.Cleanup(func() { _ = os.WriteFile(base+".gate", nil, 0o600) })
	return fmt.Sprintf("printf 'real artifact\\n' > '%s.output'; echo $$ > '%s.pid'; deadline=$((SECONDS+45)); until test -f '%s.gate'; do if (( SECONDS >= deadline )); then echo 'gate deadline exceeded' >&2; exit 1; fi; sleep 0.05; done; echo 'gate released'", base, base, base)
}

func waitTaskFile(t *testing.T, path string) string {
	t.Helper()
	var data []byte
	eventually(t, "file "+path, func() (bool, any) {
		var err error
		data, err = os.ReadFile(path)
		return err == nil && len(data) > 0, fmt.Sprintf("data=%q err=%v", data, err)
	})
	return string(data)
}

func taskPIDGone(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid shell PID %q: %v", data, err)
	}
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func waitTaskPIDGone(t *testing.T, path string) {
	t.Helper()
	eventually(t, "shell PID disappears", func() (bool, any) { return taskPIDGone(t, path), path })
}

func TestUserDelegationPreservesMCPReportAndReturnsExecutionFailure(t *testing.T) {
	// Given a real HTTP MCP server available to the background execution.
	dir := t.TempDir()
	mcpDir := t.TempDir()
	writeMCP(t, filepath.Join(dir, "agents", "default", "mcp.json"), map[string]any{"report": httpServer(t, mcpDir)})
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	d.setModel(t, "spawn")
	parent := d.newChat(t, taskJSON(t, map[string]string{"title": "MCP report", "goal": `mcp__report__append {"text":"report evidence"}`, "provider": "faux", "modelId": "tool"}))
	// When MCP finishes normally, its report is retained and delivered to the main agent.
	task := waitExecution(t, conn, parent, "MCP report", "done")
	processed := waitResultSubmission(t, conn, "task:"+task.ID+":result", "done")
	if !strings.Contains(task.Report, "appended 1") || !strings.Contains(processed.answer, "appended 1") || !strings.Contains(readNotes(t, mcpDir), "report evidence") {
		t.Fatalf("MCP report lost: task=%+v result=%+v", task, processed)
	}
	// Then a real model-boundary failure also returns a failed execution report.
	spawnExecution(t, d, parent, "Failed execution", "invalid JSON", "spawn")
	failed := waitExecution(t, conn, parent, "Failed execution", "failed")
	result := waitResultSubmission(t, conn, "task:"+failed.ID+":result", "done")
	if failed.Report != "model_error" || failed.StopNote != "" || !strings.Contains(result.content, `"status":"failed"`) || !strings.Contains(result.answer, failed.ID) {
		t.Fatalf("failure was not returned: %+v %+v", failed, result)
	}
	t.Logf("mcp=%s report=%q failed=%s report=%q", task.ID, task.Report, failed.ID, failed.Report)
}
