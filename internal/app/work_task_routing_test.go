package app_test

import (
	"fmt"
	"net/http"
	"testing"
	"uuid"

	"github.com/recally-io/dex/internal/api"
)

func TestUserSendsTaskContextToMain(t *testing.T) {
	t.Parallel()
	// Given two Tasks and a writable Main conversation.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	var main chatInfo
	d.must(t, http.MethodGet, "/api/agents/default/chats/main", nil, &main)
	const tasks = "/api/agents/default/inbox/tasks"
	var first, second api.WorkTask
	d.must(t, http.MethodPost, tasks, map[string]string{"title": "Login"}, &first, http.StatusCreated)
	d.must(t, http.MethodPost, tasks, map[string]string{"title": "Logout"}, &second, http.StatusCreated)

	// When the owner sends an input and retries it, Main sees the selected Task once.
	path := fmt.Sprintf("/api/agents/default/chats/%d/messages", main.ID)
	input := map[string]string{"id": uuid.NewV7().String(), "text": "Check the sign-in flow", "workTaskId": first.Id}
	d.must(t, http.MethodPost, path, input, nil, http.StatusAccepted)
	d.must(t, http.MethodPost, path, input, nil, http.StatusAccepted)
	d.watch(t, main.ID).until(t, "Main receives the selected Task", answeredWith("Dex Task ID: "+first.Id))
	input["workTaskId"] = second.Id
	d.must(t, http.MethodPost, path, input, nil, http.StatusConflict)

	// Then only the selected Task has a timeline entry.
	var detail api.GetWorkTask200JSONResponse
	d.must(t, http.MethodGet, tasks+"/"+first.Id, nil, &detail)
	if len(detail.Entries) != 1 || detail.Entries[0].Content != "Check the sign-in flow" {
		t.Fatalf("unexpected Task entries: %+v", detail.Entries)
	}
	d.must(t, http.MethodGet, tasks+"/"+second.Id, nil, &detail)
	if len(detail.Entries) != 0 {
		t.Fatalf("the other Task got entries: %+v", detail.Entries)
	}
}
