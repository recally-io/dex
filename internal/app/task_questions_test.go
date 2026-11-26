package app_test

import (
	"fmt"
	"net/http"
	"testing"
	"uuid"

	"github.com/recally-io/dex/internal/api"
)

func TestUserAnswersQueuedTaskQuestionsIndependently(t *testing.T) {
	t.Parallel()
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "tool")
	var main sessionListItem
	d.must(t, http.MethodGet, "/api/agents/default/sessions/main", nil, &main)
	path := fmt.Sprintf("/api/agents/default/sessions/%d/messages", main.ID)
	const tasks = "/api/tasks"

	ask := func(title string, text string, queued bool) (string, string) {
		t.Helper()
		var task api.TaskSummary
		d.must(t, http.MethodPost, tasks, map[string]string{"assigneeBotId": "default", "title": title}, &task, http.StatusCreated)
		prompt := "ask_user " + taskJSON(t, map[string]any{"question": text, "options": []string{"Yes", "No"}})
		d.must(t, http.MethodPost, path, map[string]string{"id": uuid.NewV7().String(), "text": prompt, "taskId": task.Id}, nil, http.StatusAccepted)
		eventually(t, "question on Task", func() (bool, any) {
			var detail api.TaskDetail
			d.must(t, http.MethodGet, tasks+"/"+task.Id, nil, &detail)
			return len(detail.Questions) == 1, detail.Questions
		})
		var detail api.TaskDetail
		d.must(t, http.MethodGet, tasks+"/"+task.Id, nil, &detail)
		q := detail.Questions[0]
		if q.Text != text || q.Queued != queued {
			t.Fatalf("%s: question %+v, expected queued=%t", title, q, queued)
		}
		return task.Id, q.Id
	}

	// Given one active question and two queued questions from different Tasks.
	first, firstQuestion := ask("First", "First choice?", false)
	second, secondQuestion := ask("Second", "Second choice?", true)
	_, thirdQuestion := ask("Third", "Third choice?", true)
	d.setModel(t, "echo")

	// When the owner answers the second from the Web, the first remains active.
	answer := func(task, question string) {
		t.Helper()
		d.must(t, http.MethodPost, path, map[string]string{"id": uuid.NewV7().String(), "text": "Yes", "taskId": task, "answersQuestionId": question}, nil, http.StatusAccepted)
		d.awaitIdle(t, main.ID)
	}
	answer(second, secondQuestion)
	if current := d.question(t, main.ID); current == nil || current.ID != firstQuestion {
		t.Fatalf("first question changed: %+v", current)
	}

	// Then answering the first promotes the third, not the second.
	answer(first, firstQuestion)
	if current := d.awaitQuestion(t, main.ID, "open"); current.ID != thirdQuestion {
		t.Fatalf("wrong question promoted: %+v", current)
	}
}
