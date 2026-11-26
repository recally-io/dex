package app_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/recally-io/dex/internal/api"
)

func TestUserFindsTasksAcrossBotsUntilTheirBotIsArchived(t *testing.T) {
	t.Parallel()
	// Given a Task that belongs to a second bot.
	d := startDex(t, t.TempDir(), "")
	coder := d.createCoder(t)
	const tasks = "/api/tasks"
	var task api.TaskSummary
	d.must(t, http.MethodPost, tasks, map[string]string{"assigneeBotId": coder.ID, "title": "Fix sign-in"}, &task, http.StatusCreated)
	path := tasks + "/" + task.Key

	// When the Owner reads the global list, the new Task is visible.
	// When the Owner archives its Bot, the Task leaves the list.
	visible := func() bool {
		var list api.TaskList
		d.must(t, http.MethodGet, "/api/tasks?includeClosed=true", nil, &list)
		return slices.ContainsFunc(list.Tasks, func(item api.TaskSummary) bool { return item.Id == task.Id })
	}
	var detail api.TaskDetail
	d.must(t, http.MethodGet, path, nil, &detail)
	if detail.Task.Status != api.TaskStatusReady || detail.Task.AssigneeBotId != coder.ID || len(detail.Entries) != 1 || !visible() {
		t.Fatalf("expected a ready Task of the second Bot, got %+v", detail)
	}
	archiveCoder(t, d)
	if visible() {
		t.Fatal("the Task list still shows the Task of the archived Bot")
	}
	d.must(t, http.MethodGet, path, nil, nil, http.StatusNotFound)
}
