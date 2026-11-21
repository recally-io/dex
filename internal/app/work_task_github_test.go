package app_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/db"
)

func TestUserTracksReviewsOnTheIssueTask(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := startDex(t, dir, "")
	// The bot uses a runner that is registered but not connected.
	var machine apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]string{"name": "Offline runner", "kind": "runner"}, &machine, http.StatusCreated)
	var bot apiAgent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &bot)
	bot.EnvironmentID = machine.ID
	d.must(t, http.MethodPut, "/api/agents/default", bot, &bot)
	probeRunner := func() {
		t.Helper()
		var probe apiEnvironmentTestResult
		d.must(t, http.MethodPost, "/api/settings/environments/"+machine.ID+"/test", nil, &probe)
		if probe.OK {
			t.Fatal("expected the configured runner to be offline")
		}
	}
	probeRunner()
	secret := strings.Repeat("c", 40)
	d.must(t, http.MethodPut, "/api/plugins/github/webhooks/10", map[string]string{"secret": secret, "issueAgentId": "default"}, nil, http.StatusNoContent)
	deliver := func(id, kind, body string) {
		t.Helper()
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(body))
		r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+"/hooks/github/10", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("X-GitHub-Event", kind)
		r.Header.Set("X-GitHub-Delivery", id)
		r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		resp, err := d.srv.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("%s delivery: %s", kind, resp.Status)
		}
	}

	// Given a Task created from a signed Issue delivery.
	deliver("issue-1", "issues", `{"action":"opened","repository":{"id":10},"issue":{"id":17,"number":1,"user":{"login":"maintainer"},"author_association":"MEMBER","title":"Fix login","created_at":"2026-11-21T02:00:00Z"}}`)
	var list api.ListWorkTasks200JSONResponse
	d.must(t, http.MethodGet, "/api/inbox/tasks", nil, &list)
	if len(list.Tasks) != 1 {
		t.Fatalf("expected one Issue Task: %+v", list.Tasks)
	}
	task := list.Tasks[0]
	path := "/api/agents/default/inbox/tasks/" + task.Id
	binding := map[string]any{"repositoryId": 10, "pullNumber": 2, "wait": map[string]any{
		"revision": "abc", "afterAt": time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC).UnixMilli(), "alert": true,
	}}

	// When the owner binds the PR, it cannot also belong to a second Task.
	d.must(t, http.MethodPut, "/api/plugins/github/agents/default/tasks/"+task.Id+"/pull", binding, nil, http.StatusNoContent)
	var other api.WorkTask
	d.must(t, http.MethodPost, "/api/agents/default/inbox/tasks", map[string]string{"title": "Other"}, &other, http.StatusCreated)
	d.must(t, http.MethodPut, "/api/plugins/github/agents/default/tasks/"+other.Id+"/pull", binding, nil, http.StatusConflict)

	checkWait := func(want bool) api.GetWorkTask200JSONResponse {
		t.Helper()
		var detail api.GetWorkTask200JSONResponse
		d.must(t, http.MethodGet, path, nil, &detail)
		// The Issue Task links its issue first and then the PR.
		links := detail.Links
		if len(links) != 2 || links[0].Type != "issue" || len(links[0].Waits) != 0 || links[1].Type != "pull_request" || len(links[1].Waits) != 1 || links[1].Waits[0].Resolved != want {
			t.Fatalf("wait resolution must be %t: %+v", want, links)
		}
		return detail
	}
	pushReview := func(id int, revision, submitted, association string) {
		t.Helper()
		body := fmt.Sprintf(`{"action":"submitted","repository":{"id":10},"pull_request":{"number":2},"review":{"id":%d,"state":"approved","commit_id":%q,"submitted_at":%q,"user":{"login":"reviewer"},"author_association":%q}}`, id, revision, submitted, association)
		deliver(fmt.Sprint(id), "pull_request_review", body)
	}
	pushReview(21, "abc", "2026-11-21T03:00:00Z", "MEMBER")
	pushReview(22, "old", "2026-11-21T03:01:00Z", "MEMBER")
	pushReview(24, "abc", "2026-11-21T03:02:00Z", "NONE")
	checkWait(false)
	// The Issue Task alerted Main once. Reviews that resolve no wait add no alert.
	// The runner is offline, so Main keeps the alerts as pending input.
	alerts := func(want int) {
		t.Helper()
		got := pendingTaskAlerts(t, dir)
		if len(got) != want {
			t.Fatalf("expected %d pending Task alerts in Main, got %q", want, got)
		}
		for _, alert := range got {
			if strings.Contains(alert, "Fix login") || strings.Contains(alert, "approved") {
				t.Fatalf("a Task alert carries external text: %q", alert)
			}
		}
	}
	alerts(1)
	pushReview(23, "abc", "2026-11-21T03:03:00Z", "COLLABORATOR")
	checkWait(true)
	alerts(2)

	// Then a controller restart retains the reviews and the wait result while the runner stays offline.
	d.stop()
	d = startDex(t, dir, "")
	probeRunner()
	afterRestart := checkWait(true)
	alerts(2)
	if len(afterRestart.Entries) != 5 || afterRestart.Entries[0].EventId == nil || !strings.Contains(afterRestart.Entries[0].Content, "GitHub review: approved") {
		t.Fatalf("accepted review missing after restart: %+v", afterRestart.Entries)
	}

	// A redelivery adds no duplicate and does not change the unrelated Task.
	pushReview(23, "abc", "2026-11-21T03:03:00Z", "COLLABORATOR")
	detail := checkWait(true)
	alerts(2)
	if len(detail.Entries) != 5 || detail.Entries[4].Content != "Fix login" {
		t.Fatalf("unexpected Issue Task timeline: %+v", detail.Entries)
	}
	d.must(t, http.MethodGet, "/api/agents/default/inbox/tasks/"+other.Id, nil, &detail)
	if len(detail.Entries) != 0 {
		t.Fatalf("unrelated Task received reviews: %+v", detail.Entries)
	}
}

// pendingTaskAlerts returns the content of the Task alerts that Main has not handled.
func pendingTaskAlerts(t *testing.T, dir string) []string {
	t.Helper()
	conn, err := db.Open(t.Context(), filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rows, err := conn.QueryContext(t.Context(), `SELECT s.content FROM submissions s JOIN conversations c ON c.id = s.conversation_id
JOIN work_task_inputs i ON i.submission_id = s.id WHERE c.channel_key = 'main' AND s.kind = 'task_result' AND s.status = 'pending' ORDER BY s.created_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var alerts []string
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			t.Fatal(err)
		}
		alerts = append(alerts, content)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return alerts
}
