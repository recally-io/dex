package app_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"uuid"
)

func TestUserForkOfTaskHasFreshChatPermissions(t *testing.T) {
	dir := t.TempDir()
	d := startDex(t, dir, "")
	conn := taskResultDB(t, dir)
	d.setModel(t, "spawn")
	parent := d.newChat(t, taskJSON(t, map[string]any{"title": "Report", "goal": continueReport, "provider": "faux", "modelId": "tool"}))
	task := waitExecution(t, conn, parent, "Report", "done")
	waitResultSubmission(t, conn, "task:"+task.ID+":result", "done")
	source := taskConversation(t, d, task.ID)
	path := fmt.Sprintf("/api/agents/default/chats/%d", source)
	var history struct {
		Items []struct {
			Kind    string `json:"kind"`
			EntryID int64  `json:"entryId"`
		} `json:"items"`
	}
	d.must(t, http.MethodGet, path+"/history?before=9007199254740991", nil, &history)
	var entry int64
	for _, item := range history.Items {
		if item.Kind == "user" {
			entry = item.EntryID
			break
		}
	}
	if entry == 0 {
		t.Fatal("task entry missing")
	}
	// Change settings after the entry. A fork uses these current settings.
	project := t.TempDir()
	d.must(t, http.MethodPut, path+"/settings", chatSettings{Provider: "faux", ModelID: "think", ThinkingLevel: "high"}, nil)
	d.must(t, http.MethodPut, path+"/project", map[string]string{"cwd": project}, nil)
	var child struct {
		ID       int64        `json:"id"`
		Busy     bool         `json:"busy"`
		Cwd      string       `json:"cwd"`
		Settings chatSettings `json:"settings"`
	}
	d.must(t, http.MethodPost, path+"/fork", map[string]any{"id": uuid.NewV7().String(), "entryId": entry}, &child, http.StatusCreated)
	if child.Busy || child.Cwd != project || child.Settings.ModelID != "think" || child.Settings.ThinkingLevel != "high" {
		t.Fatalf("fork settings: %+v", child)
	}
	var n int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM submissions WHERE conversation_id=?", child.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("fork ran: submissions=%d err=%v", n, err)
	}
	if answer := taskTurn(t, d, child.ID, "first message"); answer != "think high: first message" {
		t.Fatalf("first input: %s", answer)
	}
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/settings", child.ID), chatSettings{Provider: "faux", ModelID: "tool"}, nil)
	for _, call := range []string{continueReport, fmt.Sprintf(`get_task {"id":%q}`, task.ID), fmt.Sprintf(`cancel_task {"id":%q}`, task.ID)} {
		if got := taskTurn(t, d, child.ID, call); !strings.HasPrefix(got, "tool error: ") {
			t.Fatalf("inherited task authority %q: %s", call, got)
		}
	}
	var after taskInfo
	d.must(t, http.MethodGet, "/api/agents/default/tasks/"+task.ID, nil, &after)
	if after.Status != "done" || after.ReportOutcome != "done" {
		t.Fatalf("original task changed: %+v", after)
	}
	t.Logf("task=%s source=%d entry=%d fork=%d", task.ID, source, entry, child.ID)
}
