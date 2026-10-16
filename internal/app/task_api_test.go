package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
)

type taskInfo struct {
	ID                   string `json:"id"`
	Title                string `json:"title"`
	Status               string `json:"status"`
	Report               string `json:"report"`
	CreatedAt            int64  `json:"createdAt"`
	StartedAt            *int64 `json:"startedAt"`
	EndedAt              *int64 `json:"endedAt"`
	ConversationID       int64  `json:"conversationId"`
	ParentConversationID int64  `json:"parentConversationId"`
}

func TestUserReadsTaskReportsAndWork(t *testing.T) {
	// Given completed main work and running channel work with the same title.
	dir := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, dir, 42)
	d.setModel(t, "spawn")
	conn := taskResultDB(t, dir)
	main := d.newChat(t, taskJSON(t, map[string]string{"title": "Check", "goal": "saved report", "provider": "faux", "modelId": "echo"}))
	first := waitExecution(t, conn, main, "Check", "done")
	f.message(1, 42, 7, taskJSON(t, map[string]string{"title": "Check", "goal": taskGate(t, dir, "task-api"), "provider": "faux", "modelId": "bash"}))
	waitTaskFile(t, filepath.Join(dir, "task-api.pid"))
	var channel int64
	if err := conn.QueryRowContext(t.Context(), "SELECT id FROM conversations WHERE channel_key='telegram:7'").Scan(&channel); err != nil {
		t.Fatal(err)
	}
	second := waitExecution(t, conn, channel, "Check", "running")

	// When the user lists this bot's work, both conversations appear, newest first.
	var list struct {
		Tasks []taskInfo `json:"tasks"`
	}
	d.must(t, http.MethodGet, "/api/tasks", nil, &list)
	if len(list.Tasks) != 2 || list.Tasks[0].ID != second.ID || list.Tasks[1].ID != first.ID {
		t.Fatalf("tasks=%+v", list.Tasks)
	}
	if list.Tasks[0].ParentConversationID != channel || list.Tasks[1].ParentConversationID != main {
		t.Fatalf("task list lost its Stop scope: %+v", list.Tasks)
	}
	var detail taskInfo
	d.must(t, http.MethodGet, "/api/tasks/"+first.ID, nil, &detail)
	if detail.ParentConversationID != main {
		t.Fatalf("task detail lost its Stop scope: %+v", detail)
	}
	if detail.Status != "done" || detail.Report != "echo: saved report" || detail.StartedAt == nil || detail.EndedAt == nil || detail.ConversationID == 0 || detail.CreatedAt > *detail.StartedAt || *detail.StartedAt > *detail.EndedAt {
		t.Fatalf("saved task=%+v", detail)
	}
	detail = taskInfo{}
	d.must(t, http.MethodGet, "/api/tasks/"+second.ID, nil, &detail)
	if detail.Status != "running" || detail.Report != "" || detail.StartedAt == nil || detail.EndedAt != nil {
		t.Fatalf("running task=%+v", detail)
	}
	var chat struct {
		ReadOnly bool   `json:"readOnly"`
		Channel  string `json:"channel"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/chats/%d", detail.ConversationID), nil, &chat)
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
		d.must(t, method, fmt.Sprintf("/api/chats/%d/%s", detail.ConversationID, action),
			map[string]string{"id": uuid.NewV7().String(), "text": "must not run"}, nil, http.StatusForbidden)
	}
	releaseResultGate(t, dir, "task-api")
	work.until(t, "saved work answer", answeredWith("gate released"))
	d.must(t, http.MethodGet, fmt.Sprintf("/api/chats/%d/history?before=9007199254740991", detail.ConversationID), nil, nil)
	if len(d.chats(t)) != 2 {
		t.Fatal("task conversations entered the ordinary chat list")
	}
	d.must(t, http.MethodGet, "/api/tasks/missing", nil, nil, http.StatusNotFound)
	t.Logf("artifact: main task=%s channel task=%s work=%d", first.ID, second.ID, detail.ConversationID)
}

func TestUserTasksStayWithinBot(t *testing.T) {
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
	status, raw := d.call(t, http.MethodGet, "/api/tasks", nil)
	if status != http.StatusOK || strings.Contains(raw, "Other bot") || !strings.Contains(raw, `"tasks":[]`) {
		t.Fatalf("foreign task in list: %d %s", status, raw)
	}
	d.must(t, http.MethodGet, "/api/tasks/"+fmt.Sprint(work.ID), nil, nil, http.StatusNotFound)
	d.must(t, http.MethodGet, fmt.Sprintf("/api/chats/%d", work.ID), nil, nil, http.StatusNotFound)
}
