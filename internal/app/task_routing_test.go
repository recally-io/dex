package app_test

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"uuid"

	"github.com/recally-io/dex/internal/api"
)

func TestUserSendsTaskContextToMain(t *testing.T) {
	t.Parallel()
	// Given two Tasks and a writable Main conversation.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "echo")
	var main sessionListItem
	d.must(t, http.MethodGet, "/api/agents/default/sessions/main", nil, &main)
	const tasks = "/api/tasks"
	var first, second api.TaskSummary
	d.must(t, http.MethodPost, tasks, map[string]string{"assigneeBotId": "default", "title": "Login"}, &first, http.StatusCreated)
	d.must(t, http.MethodPost, tasks, map[string]string{"assigneeBotId": "default", "title": "Logout"}, &second, http.StatusCreated)

	// When the owner sends an input and retries it, Main sees the selected Task once.
	path := fmt.Sprintf("/api/agents/default/sessions/%d/messages", main.ID)
	input := map[string]string{"id": uuid.NewV7().String(), "text": "Check the sign-in flow", "taskId": first.Key}
	d.must(t, http.MethodPost, path, input, nil, http.StatusAccepted)
	input["taskId"] = first.Id
	d.must(t, http.MethodPost, path, input, nil, http.StatusAccepted)
	d.watch(t, main.ID).until(t, "Main gets Task context without changing the owner text", func(tr *transcript) bool {
		return answeredWith(`Dex Task: "Login" (ID `+first.Id+")")(tr) && slices.ContainsFunc(tr.items, func(i item) bool {
			return i.Kind == "user" && i.Text == input["text"]
		})
	})
	input["taskId"] = second.Id
	d.must(t, http.MethodPost, path, input, nil, http.StatusConflict)

	// Then only the selected Task has a timeline entry.
	var detail api.TaskDetail
	d.must(t, http.MethodGet, tasks+"/"+first.Id, nil, &detail)
	if len(detail.Entries) != 2 || detail.Entries[1].Content != "Check the sign-in flow" {
		t.Fatalf("unexpected Task entries: %+v", detail.Entries)
	}
	d.must(t, http.MethodGet, tasks+"/"+second.Id, nil, &detail)
	if len(detail.Entries) != 1 {
		t.Fatalf("the other Task got entries: %+v", detail.Entries)
	}
}

func TestUserAnswersQuestionForItsOriginalTask(t *testing.T) {
	t.Parallel()
	// Given a question from Main that is linked to the first Task.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "tool")
	var main sessionListItem
	d.must(t, http.MethodGet, "/api/agents/default/sessions/main", nil, &main)
	const tasks = "/api/tasks"
	var first, second api.TaskSummary
	d.must(t, http.MethodPost, tasks, map[string]string{"assigneeBotId": "default", "title": "Banner"}, &first, http.StatusCreated)
	d.must(t, http.MethodPost, tasks, map[string]string{"assigneeBotId": "default", "title": "Unrelated"}, &second, http.StatusCreated)
	path := fmt.Sprintf("/api/agents/default/sessions/%d/messages", main.ID)
	d.must(t, http.MethodPost, path, map[string]string{"id": uuid.NewV7().String(), "text": askJSON, "taskId": first.Id}, nil, http.StatusAccepted)
	question := d.awaitQuestion(t, main.ID, "open")
	d.awaitIdle(t, main.ID)

	// When the owner selects the wrong Task, the answer is not accepted.
	d.must(t, http.MethodPost, path, map[string]string{"id": uuid.NewV7().String(), "text": "Investigate only", "answersQuestionId": question.ID, "taskId": second.Id}, nil, http.StatusConflict)
	if current := d.question(t, main.ID); current == nil || current.ID != question.ID || current.Status != "open" {
		t.Fatalf("expected the same open question, got %+v", current)
	}

	// Then the question alone routes the answer to its original Task.
	d.setModel(t, "echo")
	d.answer(t, main.ID, question.ID, "Investigate only", http.StatusAccepted)
	d.watch(t, main.ID).until(t, "Main receives the Task of the question", answeredWith(`Dex Task: "Banner" (ID `+first.Id+")"))
	var detail api.TaskDetail
	d.must(t, http.MethodGet, tasks+"/"+first.Id, nil, &detail)
	if len(detail.Entries) != 3 || detail.Entries[2].Content != "Investigate only" {
		t.Fatalf("unexpected Task entries: %+v", detail.Entries)
	}
}
