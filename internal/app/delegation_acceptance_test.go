package app_test

import (
	"fmt"
	"net/http"
	"testing"
)

func TestUserDelegatesWithoutTaskManagementAPI(t *testing.T) {
	// Given an ordinary conversation.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	id := d.newChat(t, "hello")
	d.watch(t, id).until(t, "answer", answered("echo: hello"))

	// When a client requests the removed task management API.
	// Then the API has no task management resource.
	d.must(t, http.MethodGet, fmt.Sprintf("/api/chats/%d/tasks", id), nil, nil, http.StatusNotFound)
}
