package app_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/api"
)

func TestUserSeesGitHubCIOnlyAsATimelineEntry(t *testing.T) {
	t.Parallel()
	d := startDex(t, t.TempDir(), "")
	secret := strings.Repeat("c", 40)
	d.must(t, http.MethodPut, "/api/plugins/github/webhooks/10", map[string]any{"secret": secret, "holdUnmatched": true}, nil, http.StatusNoContent)
	var task api.TaskSummary
	d.must(t, http.MethodPost, "/api/tasks", map[string]string{"assigneeBotId": "default", "title": "Ship PR 2"}, &task, http.StatusCreated)
	path := "/api/tasks/" + task.Id
	// Given a Task that waits for a review of commit abc on PR 2 and asks for an alert.
	d.must(t, http.MethodPut, "/api/plugins/github/agents/default/tasks/"+task.Id+"/pull", map[string]any{"repositoryId": 10, "pullNumber": 2, "wait": map[string]any{"revision": "abc", "afterAt": 1, "alert": true}}, nil, http.StatusNoContent)
	suite := func(id int, updated string, pulls string) string {
		return fmt.Sprintf(`{"action":"completed","repository":{"id":10,"html_url":"https://github.com/o/r"},"check_suite":{"id":%d,"head_sha":"abc","conclusion":"failure","updated_at":%q,"app":{"slug":"github-actions"},"pull_requests":[%s]}}`, id, updated, pulls)
	}
	pr2 := `{"number":2,"base":{"repo":{"id":10}}}`

	// When CI finishes on PR 2, is redelivered, and is rerun. A fork suite and another repository's PR also arrive.
	deliverGitHub(t, d, secret, "suite-1", "check_suite", suite(5, "2026-11-21T03:00:00Z", pr2))
	deliverGitHub(t, d, secret, "suite-1-again", "check_suite", suite(5, "2026-11-21T03:00:00Z", pr2))
	deliverGitHub(t, d, secret, "suite-1-rerun", "check_suite", suite(5, "2026-11-21T03:10:00Z", pr2))
	deliverGitHub(t, d, secret, "suite-fork", "check_suite", suite(6, "2026-11-21T03:20:00Z", ""))
	deliverGitHub(t, d, secret, "suite-other", "check_suite", suite(7, "2026-11-21T03:30:00Z", `{"number":2,"base":{"repo":{"id":99}}}`))
	deliverGitHub(t, d, secret, "suite-unlinked", "check_suite", suite(8, "2026-11-21T03:40:00Z", `{"number":3,"base":{"repo":{"id":10}}}`))

	// Then the Task has one entry per completion, the wait stays open, and nothing alerts Main or waits in the Inbox.
	var detail api.TaskDetail
	d.must(t, http.MethodGet, path, nil, &detail)
	t.Logf("artifact: entries=%+v links=%+v", detail.Entries, detail.Links)
	if len(detail.Entries) != 2 || detail.Entries[1].EventId == nil || detail.Entries[1].Content != "GitHub checks: github-actions failure" {
		t.Fatalf("CI must add one external entry per completion: %+v", detail.Entries)
	}
	if waits := detail.Links[0].Waits; len(waits) != 1 || waits[0].Resolved {
		t.Fatalf("CI must not resolve a review wait: %+v", waits)
	}
	if got := pendingTaskAlerts(t, d.dataDir(t)); len(got) != 0 {
		t.Fatalf("CI alerted Main: %q", got)
	}
	var held api.ListHeldEvents200JSONResponse
	d.must(t, http.MethodGet, "/api/inbox/held-events", nil, &held)
	if len(held.Events) != 0 {
		t.Fatalf("CI on an unlinked PR must not be held: %+v", held.Events)
	}
}
