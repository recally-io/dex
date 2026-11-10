package app_test

import (
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
)

type taskInfo struct {
	ID                   string  `json:"id"`
	Title                string  `json:"title"`
	Status               string  `json:"status"`
	Report               string  `json:"report"`
	CreatedAt            int64   `json:"createdAt"`
	StartedAt            *int64  `json:"startedAt"`
	EndedAt              *int64  `json:"endedAt"`
	HandledAt            *int64  `json:"handledAt"`
	ConversationID       int64   `json:"conversationId"`
	ParentConversationID int64   `json:"parentConversationId"`
	LastAction           *string `json:"lastAction"`
	LastActivityAt       *int64  `json:"lastActivityAt"`
	ToolStartedAt        *int64  `json:"toolStartedAt"`
	ReportOutcome        string  `json:"reportOutcome"`
}

func TestUserReadsTaskReportsAndWork(t *testing.T) {
	t.Parallel()
	// Given completed main work and running channel work with the same title.
	dir := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, dir, 42)
	d.setModel(t, "spawn")
	conn := taskResultDB(t, dir)
	main := d.mainChat(t)
	d.send(t, main, taskJSON(t, map[string]string{"title": "Check", "goal": "saved report", "provider": "faux", "modelId": "echo"}))
	first := waitExecution(t, conn, main, "Check", "done")
	f.message(1, 42, 7, taskJSON(t, map[string]string{"title": "Check", "goal": taskGate(t, dir, "task-api"), "provider": "faux", "modelId": "bash"}))
	waitTaskFile(t, filepath.Join(dir, "task-api.pid"))
	var channel int64
	if err := conn.QueryRowContext(t.Context(), "SELECT id FROM conversations WHERE channel_key='main'").Scan(&channel); err != nil {
		t.Fatal(err)
	}
	second := waitExecution(t, conn, channel, "Check", "running")

	// When the user lists this bot's work, both conversations appear, newest first.
	status, raw := d.call(t, http.MethodGet, "/api/agents/default/chats", nil)
	if status != http.StatusOK || strings.Contains(raw, `"report":`) {
		t.Fatalf("task list must omit report bodies: %d %s", status, raw)
	}
	var list struct {
		Chats []sessionInfo `json:"chats"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/chats", nil, &list)
	var tasks []taskInfo
	for _, c := range list.Chats {
		if c.Task != nil {
			tasks = append(tasks, *c.Task)
		}
	}
	if len(tasks) != 2 || tasks[0].ID != second.ID || tasks[1].ID != first.ID {
		t.Fatalf("tasks=%+v", tasks)
	}
	if tasks[0].LastAction == nil || tasks[0].LastActivityAt == nil || tasks[0].ToolStartedAt == nil {
		t.Fatalf("running task activity missing from ListChats without events: %+v", tasks[0])
	}
	// The main agent reads the same live activity through get_task.
	answer := taskTool(t, d, channel, "get_task", map[string]string{"id": second.ID})
	var live taskInfo
	if err := json.Unmarshal([]byte(strings.TrimPrefix(answer, "tool: ")), &live); err != nil || live.LastAction == nil || !strings.HasPrefix(*live.LastAction, "bash: ") || live.LastActivityAt == nil || live.ToolStartedAt == nil {
		t.Fatalf("get_task lacks live activity: %s %v", answer, err)
	}
	if tasks[0].ParentConversationID != channel || tasks[1].ParentConversationID != main {
		t.Fatalf("session list lost its Stop scope: %+v", tasks)
	}
	var detail taskInfo
	d.must(t, http.MethodGet, "/api/agents/default/tasks/"+first.ID, nil, &detail)
	if detail.ParentConversationID != main {
		t.Fatalf("task detail lost its Stop scope: %+v", detail)
	}
	if detail.Status != "done" || detail.Report != "echo: saved report" || detail.StartedAt == nil || detail.EndedAt == nil || detail.ConversationID == 0 || detail.CreatedAt > *detail.StartedAt || *detail.StartedAt > *detail.EndedAt {
		t.Fatalf("saved task=%+v", detail)
	}
	detail = taskInfo{}
	d.must(t, http.MethodGet, "/api/agents/default/tasks/"+second.ID, nil, &detail)
	if detail.Status != "running" || detail.Report != "" || detail.StartedAt == nil || detail.EndedAt != nil {
		t.Fatalf("running task=%+v", detail)
	}
	var chat struct {
		ReadOnly bool   `json:"readOnly"`
		Channel  string `json:"channel"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", detail.ConversationID), nil, &chat)
	if !chat.ReadOnly || chat.Channel != "task" {
		t.Fatalf("task chat=%+v", chat)
	}
	work := d.watch(t, detail.ConversationID)
	work.until(t, "live read-only work", func(tr *transcript) bool { return tr.running })
	// Then the work stream is readable, but no web input or control is allowed.
	for _, action := range []string{"messages", "clear", "abort", "settings"} {
		method := http.MethodPost
		if action == "settings" {
			method = http.MethodPut
		}
		d.must(t, method, fmt.Sprintf("/api/agents/default/chats/%d/%s", detail.ConversationID, action),
			map[string]string{"id": uuid.NewV7().String(), "text": "must not run"}, nil, http.StatusForbidden)
	}
	releaseResultGate(t, dir, "task-api")
	work.until(t, "saved work answer", answeredWith("gate released"))
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d/history?before=9007199254740991", detail.ConversationID), nil, nil)
	if len(d.chats(t)) != 2 {
		t.Fatal("Sessions must contain both tasks, without main")
	}
	d.must(t, http.MethodGet, "/api/agents/default/tasks/missing", nil, nil, http.StatusNotFound)
	t.Logf("artifact: main task=%s channel task=%s work=%d", first.ID, second.ID, detail.ConversationID)
}

func TestUserAcknowledgesFinishedTaskWithoutChangingItsReport(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	parent := d.mainChat(t)
	d.setModel(t, "spawn")
	taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "Finished work", "goal": `report_task {"outcome":"blocked","summary":"original result","cause":"needs attention","reproduction":[],"files":[],"openItems":[]}`, "provider": "faux", "modelId": "tool"}))
	finished := waitExecution(t, conn, parent, "Finished work", "done")

	// The agent acknowledges the finished task through its native tool.
	answer := taskTool(t, d, parent, "acknowledge_task", map[string]string{"id": finished.ID})
	var toolResult taskInfo
	if err := json.Unmarshal([]byte(strings.TrimPrefix(answer, "tool: ")), &toolResult); err != nil || toolResult.HandledAt == nil || !strings.Contains(toolResult.Report, "original result") {
		t.Fatalf("tool result=%s err=%v", answer, err)
	}
	var detail taskInfo
	d.must(t, http.MethodPost, "/api/agents/default/tasks/"+finished.ID+"/acknowledge", nil, &detail)
	if detail.HandledAt == nil || *detail.HandledAt != *toolResult.HandledAt || detail.Status != "done" || detail.ReportOutcome != "blocked" || detail.Report != toolResult.Report {
		t.Fatalf("idempotent acknowledgement changed the result: %+v", detail)
	}
	var list struct {
		Chats []sessionInfo `json:"chats"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/chats", nil, &list)
	found := false
	for _, c := range list.Chats {
		if c.Task != nil && c.Task.ID == finished.ID {
			found = c.Task.HandledAt != nil && *c.Task.HandledAt == *detail.HandledAt
		}
	}
	if !found {
		t.Fatal("acknowledgement not returned in task list")
	}

	// Running tasks cannot be acknowledged and missing tasks stay inaccessible.
	d.setModel(t, "spawn")
	taskTurn(t, d, parent, taskJSON(t, map[string]any{"title": "Running work", "goal": taskGate(t, dir, "ack-running"), "provider": "faux", "modelId": "bash"}))
	waitTaskFile(t, filepath.Join(dir, "ack-running.pid"))
	running := waitExecution(t, conn, parent, "Running work", "running")
	d.must(t, http.MethodPost, "/api/agents/default/tasks/"+running.ID+"/acknowledge", nil, nil, http.StatusConflict)
	d.must(t, http.MethodPost, "/api/agents/default/tasks/missing/acknowledge", nil, nil, http.StatusNotFound)
	releaseResultGate(t, dir, "ack-running")
}

func TestUserTasksStayWithinBot(t *testing.T) {
	t.Parallel()
	// Given a saved task owned by another bot in the same control database.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "control.sqlite")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	if err := q.CreateAgentIfMissing(t.Context(), sqlc.CreateAgentIfMissingParams{ID: "other", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	parent, err := q.EnsureConversation(t.Context(), sqlc.EnsureConversationParams{AgentID: "other", ChannelKey: "main"})
	if err != nil {
		t.Fatal(err)
	}
	work, err := q.EnsureConversation(t.Context(), sqlc.EnsureConversationParams{AgentID: "other", ChannelKey: "task:foreign"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []int64{parent.ID, work.ID} {
		id := fmt.Sprint(c)
		if _, err := q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{ID: id, ConversationID: c, Attachments: "null", Kind: "message", Origin: "task"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.CreateTask(t.Context(), sqlc.CreateTaskParams{ID: fmt.Sprint(work.ID), ParentConversationID: parent.ID, ConversationID: work.ID, SpawnKey: "foreign", SourceRequestID: fmt.Sprint(parent.ID), Title: "Other bot"}); err != nil {
		t.Fatal(err)
	}
	// When the default bot's user reads tasks, the other bot's work stays outside this API.
	status, raw := d.call(t, http.MethodGet, "/api/agents/default/chats", nil)
	if status != http.StatusOK || strings.Contains(raw, "Other bot") || !strings.Contains(raw, `"chats":[]`) {
		t.Fatalf("foreign task in list: %d %s", status, raw)
	}
	d.must(t, http.MethodGet, "/api/agents/default/tasks/"+fmt.Sprint(work.ID), nil, nil, http.StatusNotFound)
	d.must(t, http.MethodPost, "/api/agents/default/tasks/"+fmt.Sprint(work.ID)+"/acknowledge", nil, nil, http.StatusNotFound)
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", work.ID), nil, nil, http.StatusNotFound)
}

func TestUserTaskSummariesDoNotOpenColdBots(t *testing.T) {
	t.Parallel()
	// Given a bot that has never run.
	d := startDex(t, t.TempDir(), "")
	var b apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Cold", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &b, http.StatusCreated)
	state := filepath.Join(d.dataDir(t), "agents", b.ID, "state.sqlite")
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new bot already has native state: %v", err)
	}
	// When the task panel polls its chats, the bot's native state stays closed.
	for range 3 {
		d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/%s/chats", b.ID), nil, nil)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("listing chats opened native state: %v", err)
	}
}
