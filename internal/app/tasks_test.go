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
)

type taskStage struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	ModelID     string `json:"modelId"`
	CurrentTool string `json:"currentTool"`
	Report      string `json:"report"`
}

type userTask struct {
	ID      string      `json:"id"`
	ChatID  int64       `json:"chatId"`
	ShortID string      `json:"shortId"`
	Title   string      `json:"title"`
	Status  string      `json:"status"`
	Report  string      `json:"report"`
	Stages  []taskStage `json:"stages"`
}

func (d *dex) tasks(t *testing.T, chatID int64) []userTask {
	t.Helper()
	var out struct {
		Tasks []userTask `json:"tasks"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/chats/%d/tasks", chatID), nil, &out)
	return out.Tasks
}

func TestUserCanQueryTasksWithoutAModelCall(t *testing.T) {
	// Given a chat with no background work.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	chatID := d.newChat(t, "hello")
	d.watch(t, chatID).until(t, "first answer", answered("echo: hello"))
	// When the user opens the task list.
	tasks := d.tasks(t, chatID)
	// Then control returns an empty list without starting a model turn.
	if len(tasks) != 0 {
		t.Fatalf("tasks: %+v", tasks)
	}
}

func TestUserReviewsBackgroundArtifactBeforeFinishingGoal(t *testing.T) {
	// Given a shell stage with a real artifact and a file-controlled gate.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "spawn")
	chatID := d.newChat(t, taskJSON(t, map[string]string{
		"title": "Artifact", "goal": taskGate(t, dir, "artifact"), "provider": "faux", "modelId": "bash",
	}))
	task := waitUserTask(t, d, chatID, "Artifact", "running", 1)
	waitTaskFile(t, filepath.Join(dir, "artifact.pid"))
	if data := waitTaskFile(t, filepath.Join(dir, "artifact.output")); data != "real artifact\n" {
		t.Fatalf("shell artifact: %q", data)
	}

	// When the main chat changes model, it can answer while the shell waits.
	setTaskChatModel(t, d, chatID, "echo")
	if got := taskTurn(t, d, chatID, "foreground remains available"); got != "echo: foreground remains available" {
		t.Fatal(got)
	}
	if got := getUserTask(t, d, chatID, task.ID); got.Stages[0].Status != "running" {
		t.Fatalf("stage ended before gate opened: %+v", got)
	}
	setTaskChatModel(t, d, chatID, "spawn")
	w := d.watch(t, chatID)
	if err := os.WriteFile(filepath.Join(dir, "artifact.gate"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	w.until(t, "result in source chat", answeredWith(`"stageId":"`+task.Stages[0].ID+`"`))
	var result struct {
		ID      string `json:"id"`
		ShortID string `json:"shortId"`
		StageID string `json:"stageId"`
		Status  string `json:"status"`
		Report  string `json:"report"`
	}
	last := w.tr.items[len(w.tr.items)-1].Text
	if err := json.Unmarshal([]byte(strings.TrimPrefix(last, "task-result: ")), &result); err != nil {
		t.Fatalf("result envelope %q: %v", last, err)
	}
	if result.ID != task.ID || result.ShortID != task.ShortID || result.StageID != task.Stages[0].ID || result.Status != "done" || !strings.Contains(result.Report, "gate released") {
		t.Fatalf("result envelope: %+v", result)
	}
	task = waitUserTask(t, d, chatID, "Artifact", "done", 1)
	if task.Status != "active" || task.ChatID != chatID || task.Stages[0].ModelID != "bash" || task.Stages[0].Report != result.Report {
		t.Fatalf("stage completion must leave goal active: %+v", task)
	}

	// Then task tools expose the report before explicit review closes the goal.
	if got := taskTool(t, d, chatID, "list_tasks", map[string]string{}); !strings.Contains(got, task.ID) {
		t.Fatalf("list_tasks omitted goal: %s", got)
	}
	if got := taskTool(t, d, chatID, "get_task", map[string]string{"id": task.ShortID}); !strings.Contains(got, "gate released") {
		t.Fatalf("get_task omitted stage report: %s", got)
	}
	report := "Reviewed artifact: " + filepath.Join(dir, "artifact.output")
	taskTool(t, d, chatID, "finish_task", map[string]string{"id": task.ID, "status": "done", "report": report})
	if got := getUserTask(t, d, chatID, task.ShortID); got.Status != "done" || got.Report != report || got.Stages[0].Report != result.Report {
		t.Fatalf("saved review: %+v", got)
	}
	if chats := d.chats(t); len(chats) != 1 || chats[0].ID != chatID {
		t.Fatalf("hidden task leaked into chat list: %+v", chats)
	}
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var hiddenID int64
	if err := conn.QueryRowContext(t.Context(), "SELECT conversation_id FROM task_stages WHERE id = ?", task.Stages[0].ID).Scan(&hiddenID); err != nil {
		t.Fatal(err)
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/chats/%d", hiddenID), nil, nil, http.StatusNotFound)
}

func TestUserTaskSlotsAreSharedAndNeedsInputReleasesCapacity(t *testing.T) {
	// Given a completed stage whose goal needs a user decision.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "spawn")
	chatID := d.newChat(t, taskJSON(t, map[string]string{"title": "Decision", "goal": "review choice", "provider": "faux", "modelId": "echo"}))
	decision := waitUserTask(t, d, chatID, "Decision", "done", 1)
	report := "Choose the release target before work continues."
	taskTool(t, d, chatID, "finish_task", map[string]string{"id": decision.ID, "status": "needs_input", "report": report})

	// When two chats on the same agent fill both slots, another spawn fails.
	spawnUserTask(t, d, chatID, "First slot", taskGate(t, dir, "first"), "bash", "")
	first := waitUserTask(t, d, chatID, "First slot", "running", 1)
	waitTaskFile(t, filepath.Join(dir, "first.pid"))
	d.setModel(t, "spawn")
	otherChat := d.newChat(t, taskJSON(t, map[string]string{"title": "Second slot", "goal": taskGate(t, dir, "second"), "provider": "faux", "modelId": "bash"}))
	second := waitUserTask(t, d, otherChat, "Second slot", "running", 1)
	waitTaskFile(t, filepath.Join(dir, "second.pid"))
	got := spawnUserTask(t, d, chatID, "Rejected", "must not run", "echo", "")
	if !strings.Contains(got, "error") || !strings.Contains(got, "two occupied task slots") || len(d.tasks(t, chatID)) != 2 {
		t.Fatalf("third task was not rejected: %s; tasks=%+v", got, d.tasks(t, chatID))
	}
	if got := getUserTask(t, d, chatID, decision.ID); got.Status != "needs_input" || got.Report != report {
		t.Fatalf("needs_input report changed while slots filled: %+v", got)
	}

	// Then cancellation releases capacity, but task stages cannot nest tasks.
	cancelUserTask(t, d, chatID, first.ID)
	waitUserTask(t, d, chatID, "First slot", "cancelled", 1)
	waitTaskPIDGone(t, filepath.Join(dir, "first.pid"))
	spawnUserTask(t, d, chatID, "Nested attempt", "spawn_task "+taskJSON(t, map[string]string{"title": "Forbidden child", "goal": "must not run", "provider": "faux", "modelId": "echo"}), "tool", "")
	nested := waitUserTask(t, d, chatID, "Nested attempt", "done", 1)
	if !strings.Contains(nested.Stages[0].Report, "task stages cannot spawn") || len(d.tasks(t, chatID)) != 3 {
		t.Fatalf("nested spawn was not rejected: %+v; tasks=%+v", nested, d.tasks(t, chatID))
	}
	if got := taskTool(t, d, otherChat, "cancel_task", map[string]string{"id": second.ID}); strings.Contains(got, "tool error:") {
		t.Fatalf("cancel_task failed: %s", got)
	}
	waitUserTask(t, d, otherChat, "Second slot", "cancelled", 1)
	waitTaskPIDGone(t, filepath.Join(dir, "second.pid"))
}

func TestUserCancelsLaterStageWithoutConfiguredModel(t *testing.T) {
	// Given a goal that completed one stage and is running a second shell stage.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "spawn")
	chatID := d.newChat(t, taskJSON(t, map[string]string{"title": "Multi-stage", "goal": "first report", "provider": "faux", "modelId": "echo"}))
	task := waitUserTask(t, d, chatID, "Multi-stage", "done", 1)
	firstReport := task.Stages[0].Report
	spawnUserTask(t, d, chatID, "Later stage", taskGate(t, dir, "later"), "bash", task.ID)
	waitUserTask(t, d, chatID, "Multi-stage", "running", 2)
	waitTaskFile(t, filepath.Join(dir, "later.pid"))

	// When all model settings are cleared, HTTP query and cancel still work.
	d.must(t, http.MethodPut, fmt.Sprintf("/api/chats/%d/settings", chatID), map[string]string{"provider": "", "modelId": "", "thinkingLevel": ""}, nil)
	d.must(t, http.MethodPut, "/api/settings/agent", map[string]string{"provider": "", "modelId": "", "thinkingLevel": "off", "instructions": ""}, nil)
	if got := getUserTask(t, d, chatID, task.ShortID); got.Stages[0].Report != firstReport {
		t.Fatalf("report unavailable without model: %+v", got)
	}
	cancelUserTask(t, d, chatID, task.ShortID)
	eventually(t, "cancelled only after foreground shell exits", func() (bool, any) {
		got := getUserTask(t, d, chatID, task.ID)
		gone := taskPIDGone(t, filepath.Join(dir, "later.pid"))
		if got.Status == "cancelled" && !gone {
			t.Fatalf("goal marked cancelled while shell PID exists: %+v", got)
		}
		return got.Status == "cancelled" && gone, got
	})
	task = waitUserTask(t, d, chatID, "Multi-stage", "cancelled", 2)
	if task.Stages[0].Report != firstReport || task.Report == "" {
		t.Fatalf("cancel lost reports: %+v", task)
	}

	// Then restoring a model cannot reopen the cancelled goal by adding a stage.
	d.setModel(t, "spawn")
	got := spawnUserTask(t, d, chatID, "Forbidden continuation", "must not run", "echo", task.ID)
	if !strings.Contains(got, "error") || !strings.Contains(got, "stopped, closed, or has unknown execution") {
		t.Fatalf("cancelled goal accepted another stage: %s", got)
	}
	if got := getUserTask(t, d, chatID, task.ID); len(got.Stages) != 2 || got.Status != "cancelled" {
		t.Fatalf("cancelled goal changed: %+v", got)
	}
}

func TestUserRestartKeepsUnknownStageWithoutRerunningShell(t *testing.T) {
	// Given a running shell whose counter records every execution.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "spawn")
	goal := "printf 'run\\n' >> '" + filepath.Join(dir, "counter") + "'; " + taskGate(t, dir, "restart")
	chatID := d.newChat(t, taskJSON(t, map[string]string{"title": "Interrupted", "goal": goal, "provider": "faux", "modelId": "bash"}))
	task := waitUserTask(t, d, chatID, "Interrupted", "running", 1)
	waitTaskFile(t, filepath.Join(dir, "restart.pid"))
	if got := waitTaskFile(t, filepath.Join(dir, "counter")); got != "run\n" {
		t.Fatalf("initial execution counter: %q", got)
	}

	// When control and its embedded worker restart against the same SQLite storage.
	d.stop()
	d = startDex(t, dir, "")
	task = waitUserTask(t, d, chatID, "Interrupted", "unknown", 1)
	if task.Status != "unknown" || task.Stages[0].Report == "" {
		t.Fatalf("restart concealed uncertain execution: %+v", task)
	}

	// Then the unknown stage occupies one slot, and CLI execution does not repeat.
	spawnUserTask(t, d, chatID, "Remaining slot", taskGate(t, dir, "remaining"), "bash", "")
	remaining := waitUserTask(t, d, chatID, "Remaining slot", "running", 1)
	waitTaskFile(t, filepath.Join(dir, "remaining.pid"))
	got := spawnUserTask(t, d, chatID, "No capacity", "must not run", "echo", "")
	if !strings.Contains(got, "two occupied task slots") {
		t.Fatalf("unknown stage did not occupy capacity: %s", got)
	}
	cancelUserTask(t, d, chatID, remaining.ID)
	waitUserTask(t, d, chatID, "Remaining slot", "cancelled", 1)
	waitTaskPIDGone(t, filepath.Join(dir, "remaining.pid"))
	if got := waitTaskFile(t, filepath.Join(dir, "counter")); got != "run\n" {
		t.Fatalf("restart reran external CLI: %q", got)
	}
	if got := getUserTask(t, d, chatID, task.ID); got.Status != "unknown" || got.Stages[0].Status != "unknown" || len(got.Stages) != 1 {
		t.Fatalf("unknown execution changed after new work: %+v", got)
	}
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

func spawnUserTask(t *testing.T, d *dex, chatID int64, title, goal, model, taskID string) string {
	t.Helper()
	setTaskChatModel(t, d, chatID, "spawn")
	args := map[string]string{"title": title, "goal": goal, "provider": "faux", "modelId": model}
	if taskID != "" {
		args["userTaskId"] = taskID
	}
	return taskTurn(t, d, chatID, taskJSON(t, args))
}

func getUserTask(t *testing.T, d *dex, chatID int64, id string) userTask {
	t.Helper()
	var task userTask
	d.must(t, http.MethodGet, fmt.Sprintf("/api/chats/%d/tasks/%s", chatID, id), nil, &task)
	return task
}

func cancelUserTask(t *testing.T, d *dex, chatID int64, id string) {
	t.Helper()
	d.must(t, http.MethodPost, fmt.Sprintf("/api/chats/%d/tasks/%s/cancel", chatID, id), nil, nil, http.StatusAccepted)
}

func waitUserTask(t *testing.T, d *dex, chatID int64, title, stageStatus string, stageCount int) userTask {
	t.Helper()
	var found userTask
	eventually(t, title+" stage "+stageStatus, func() (bool, any) {
		tasks := d.tasks(t, chatID)
		for _, task := range tasks {
			if task.Title == title {
				found = task
				return len(task.Stages) == stageCount && task.Stages[stageCount-1].Status == stageStatus, task
			}
		}
		return false, tasks
	})
	return found
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
