package app_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/api"
)

func TestUserAttachesAndDismissesHeldGitHubEvents(t *testing.T) {
	t.Parallel()
	d := startDex(t, t.TempDir(), "")
	secret := strings.Repeat("h", 40)
	// Given a repository rule that holds unmatched events and creates no Task.
	d.must(t, http.MethodPut, "/api/plugins/github/webhooks/10", map[string]any{"secret": secret, "holdUnmatched": true}, nil, http.StatusNoContent)
	review := func(id, pull int, association string) string {
		return fmt.Sprintf(`{"action":"submitted","repository":{"id":10},"pull_request":{"number":%d},"review":{"id":%d,"state":"commented","body":"Review %d","commit_id":"abc","submitted_at":"2026-11-21T03:00:00Z","user":{"login":"reviewer"},"author_association":%q,"html_url":"https://github.com/o/r/pull/%d"}}`, pull, id, id, association, pull)
	}

	// When a member reviews an unlinked PR, an outsider reviews another one, and a member opens an Issue.
	deliverGitHub(t, d, secret, "review-1", "pull_request_review", review(1, 7, "MEMBER"))
	deliverGitHub(t, d, secret, "review-2", "pull_request_review", review(2, 8, "NONE"))
	deliverGitHub(t, d, secret, "issue-1", "issues", `{"action":"opened","repository":{"id":10},"issue":{"id":17,"number":1,"user":{"login":"maintainer"},"author_association":"MEMBER","title":"Fix login","html_url":"https://github.com/o/r/issues/1","created_at":"2026-11-21T02:00:00Z"}}`)
	deliverGitHub(t, d, secret, "review-1-again", "pull_request_review", review(1, 7, "MEMBER"))

	// Then the Inbox holds the two member events once each, and no Task exists yet.
	var held api.ListHeldEvents200JSONResponse
	d.must(t, http.MethodGet, "/api/inbox/held-events", nil, &held)
	t.Logf("artifact: held=%+v", held.Events)
	types := map[string]api.HeldEvent{}
	for _, e := range held.Events {
		types[e.EventType] = e
	}
	pr, issue := types["review.submitted"], types["issue.opened"]
	if len(held.Events) != 2 || pr.Reason != api.Unmatched || pr.AuthorAssociation != "MEMBER" || !strings.Contains(pr.Content, "Review 1") ||
		len(pr.Objects) != 1 || pr.Objects[0].Key != "7" || issue.Id == "" {
		t.Fatalf("the Inbox must hold the member review and Issue: %+v", held.Events)
	}
	var tasks api.ListWorkTasks200JSONResponse
	d.must(t, http.MethodGet, "/api/inbox/tasks", nil, &tasks)
	if len(tasks.Tasks) != 0 {
		t.Fatalf("a held event must not create a Task: %+v", tasks.Tasks)
	}

	// When the owner attaches the review to a new Task and dismisses the Issue.
	var task api.WorkTask
	d.must(t, http.MethodPost, "/api/agents/default/inbox/tasks", map[string]string{"title": "Ship PR 7"}, &task, http.StatusCreated)
	attach := map[string]string{"agentId": "default", "taskId": task.Id}
	d.must(t, http.MethodPost, "/api/inbox/held-events/"+pr.Id+"/attach", attach, nil, http.StatusNoContent)
	d.must(t, http.MethodPost, "/api/inbox/held-events/"+pr.Id+"/attach", attach, nil, http.StatusNotFound)
	d.must(t, http.MethodPost, "/api/inbox/held-events/"+issue.Id+"/dismiss", nil, nil, http.StatusNoContent)
	d.must(t, http.MethodPost, "/api/inbox/held-events/"+issue.Id+"/dismiss", nil, nil, http.StatusNotFound)

	// Then the Inbox is empty, the Task shows the review, and the PR now routes to the Task.
	d.must(t, http.MethodGet, "/api/inbox/held-events", nil, &held)
	if len(held.Events) != 0 {
		t.Fatalf("decided events stay in the Inbox: %+v", held.Events)
	}
	deliverGitHub(t, d, secret, "review-3", "pull_request_review", review(3, 7, "MEMBER"))
	var detail api.GetWorkTask200JSONResponse
	d.must(t, http.MethodGet, "/api/agents/default/inbox/tasks/"+task.Id, nil, &detail)
	t.Logf("artifact: task entries=%+v links=%+v", detail.Entries, detail.Links)
	if len(detail.Entries) != 2 || !strings.Contains(detail.Entries[0].Content, "Review 3") || !strings.Contains(detail.Entries[1].Content, "Review 1") {
		t.Fatalf("the Task must show the attached review and the next one: %+v", detail.Entries)
	}
	if len(detail.Links) != 1 || detail.Links[0].Type != "pull_request" || detail.Links[0].Key != "7" {
		t.Fatalf("the attach must link the PR: %+v", detail.Links)
	}
	d.must(t, http.MethodGet, "/api/inbox/held-events", nil, &held)
	if len(held.Events) != 0 {
		t.Fatalf("a routed review must not be held: %+v", held.Events)
	}
	// An owner decision is not an alert rule.
	if got := pendingTaskAlerts(t, d.dataDir(t)); len(got) != 0 {
		t.Fatalf("a held event alerted Main: %q", got)
	}
}
