package app_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/recally-io/dex/internal/api"
)

func TestUserCanAccessTasksOnlyThroughTheirBot(t *testing.T) {
	t.Parallel()
	// Given a Task that belongs to a second bot.
	d := startDex(t, t.TempDir(), "")
	coder := d.createCoder(t)
	ownerPath := "/api/agents/" + coder.ID + "/inbox/tasks"
	var task api.WorkTask
	d.must(t, http.MethodPost, ownerPath, map[string]string{"title": "Fix sign-in"}, &task, http.StatusCreated)

	// When the owner uses the wrong bot's URL, the Task cannot be read or changed.
	wrongPath := "/api/agents/default/inbox/tasks/" + task.Id
	d.must(t, http.MethodGet, wrongPath, nil, nil, http.StatusNotFound)
	d.must(t, http.MethodPost, wrongPath, map[string]string{"content": "Wrong bot"}, nil, http.StatusNotFound)
	d.must(t, http.MethodPatch, wrongPath, map[string]string{"lifecycle": "completed"}, nil, http.StatusNotFound)

	// Then the Task is still open, and archiving its bot hides it from Inbox.
	visible := func() bool {
		var list api.ListWorkTasks200JSONResponse
		d.must(t, http.MethodGet, "/api/inbox/tasks?includeClosed=true", nil, &list)
		return slices.ContainsFunc(list.Tasks, func(item api.WorkTask) bool { return item.Id == task.Id })
	}
	var detail api.GetWorkTask200JSONResponse
	d.must(t, http.MethodGet, ownerPath+"/"+task.Id, nil, &detail)
	if detail.Task.Lifecycle != "open" || len(detail.Entries) != 0 || !visible() {
		t.Fatalf("expected an open Task without notes in Inbox, got %+v", detail)
	}
	archiveCoder(t, d)
	if visible() {
		t.Fatal("Inbox still lists the Task of the archived bot")
	}
	d.must(t, http.MethodGet, ownerPath+"/"+task.Id, nil, nil, http.StatusNotFound)
}
