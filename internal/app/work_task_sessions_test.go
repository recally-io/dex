package app_test

import (
	"fmt"
	"net/http"
	"testing"
	"uuid"

	"github.com/recally-io/dex/internal/api"
	worktasktools "github.com/recally-io/dex/internal/worktask/tools"
)

func TestUserTracksSessionsFromTwoBotsOnOneTask(t *testing.T) {
	t.Parallel()
	// Given a Task owned by the default Bot and a second Bot.
	d := startDex(t, t.TempDir(), "")
	coder := d.createCoder(t)
	var main chatInfo
	d.must(t, http.MethodGet, "/api/agents/default/chats/main", nil, &main)
	var task api.WorkTask
	d.must(t, http.MethodPost, "/api/agents/default/inbox/tasks", map[string]string{"title": "Ship login"}, &task, http.StatusCreated)
	path := "/api/agents/default/inbox/tasks/" + task.Id
	var detail api.GetWorkTask200JSONResponse
	d.setModel(t, "tool")
	// When Task inputs start a child in each Bot.
	start := func(title, bot, taskID string) string {
		t.Helper()
		d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/messages", main.ID), map[string]string{
			"id": uuid.NewV7().String(), "workTaskId": taskID, "text": "session_new " + taskJSON(t, map[string]string{
				"title": title, "bot": bot, "message": "Work on login", "provider": "faux", "modelId": "echo",
			}),
		}, nil, http.StatusAccepted)
		conn := sessionDB(t, d)
		turn := waitExecution(t, conn, main.ID, title, "done")
		eventually(t, "the child result settles in Main", func() (bool, any) {
			var status string
			err := conn.QueryRowContext(t.Context(), `SELECT status FROM submissions WHERE id = ?`, turn.ResultID).Scan(&status)
			return err == nil && status != "pending", fmt.Sprintf("status=%s err=%v", status, err)
		})
		return turn.ID
	}
	start("Implement login", "", task.Id)
	start("Review login", "Coder", task.Id)

	// Then the Board keeps both links and the first Session stays primary.
	d.must(t, http.MethodGet, path, nil, &detail)
	if len(detail.Sessions) != 2 {
		t.Fatalf("Board must show both sessions: %+v", detail.Sessions)
	}
	first, second := detail.Sessions[0], detail.Sessions[1]
	if first.AgentId != "default" || first.Title != "Implement login" || !first.IsPrimary || first.LinkedAt == 0 ||
		second.AgentId != coder.ID || second.AgentName != "Coder" || second.Title != "Review login" || second.IsPrimary || second.LinkedAt == 0 ||
		detail.Session == nil || detail.Session.Id != first.ConversationId {
		t.Fatalf("unexpected session links: %+v, legacy primary: %+v", detail.Sessions, detail.Session)
	}

	// And the Owner Bot can link a cross-Bot child started outside the Task and promote it later.
	child := start("Check login", "Coder", "")
	var updated worktasktools.WorkTaskInfo
	taskToolJSON(t, d, main.ID, "task_update", map[string]any{"id": task.Id, "session": child}, &updated)
	if len(updated.Sessions) != 3 || updated.Session == nil || updated.Session.ID != fmt.Sprint(first.ConversationId) {
		t.Fatalf("adding a child must keep the current primary and both links: %+v", updated)
	}
	taskToolJSON(t, d, main.ID, "task_update", map[string]any{"id": task.Id, "session": child, "primary": true}, &updated)
	var tool worktasktools.WorkTaskDetail
	taskToolJSON(t, d, main.ID, "task_read", map[string]string{"id": task.Id}, &tool)
	if len(tool.Sessions) != 3 || tool.Sessions[0].ID != child || tool.Sessions[0].BotID != coder.ID ||
		tool.Sessions[0].BotName != "Coder" || !tool.Sessions[0].IsPrimary || tool.Sessions[1].IsPrimary || tool.Sessions[2].IsPrimary {
		t.Fatalf("task_read must show all sessions after promotion: %+v", tool.Sessions)
	}
}
