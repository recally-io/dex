package app_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/api"
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
	d.must(t, http.MethodPut, "/api/settings/github-webhooks/10", map[string]string{"secret": secret, "issueAgentId": "default"}, nil, http.StatusNoContent)
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
	deliver("issue-1", "issues", `{"action":"opened","repository":{"id":10},"issue":{"id":17,"number":1,"title":"Fix login","created_at":"2026-11-21T02:00:00Z"}}`)
	var list api.ListWorkTasks200JSONResponse
	d.must(t, http.MethodGet, "/api/inbox/tasks", nil, &list)
	if len(list.Tasks) != 1 {
		t.Fatalf("expected one Issue Task: %+v", list.Tasks)
	}
	task := list.Tasks[0]
	path := "/api/agents/default/inbox/tasks/" + task.Id
	binding := map[string]any{"repositoryId": 10, "pullNumber": 2, "wait": map[string]any{
		"revision": "abc", "afterAt": time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC).UnixMilli(),
	}}

	// When the owner binds the PR, it cannot also belong to a second Task.
	d.must(t, http.MethodPut, path+"/github-pull", binding, nil, http.StatusNoContent)
	var other api.WorkTask
	d.must(t, http.MethodPost, "/api/agents/default/inbox/tasks", map[string]string{"title": "Other"}, &other, http.StatusCreated)
	d.must(t, http.MethodPut, "/api/agents/default/inbox/tasks/"+other.Id+"/github-pull", binding, nil, http.StatusConflict)

	checkWait := func(want bool) api.GetWorkTask200JSONResponse {
		t.Helper()
		var detail api.GetWorkTask200JSONResponse
		d.must(t, http.MethodGet, path, nil, &detail)
		pulls := detail.GithubPulls
		if len(pulls) != 1 || len(pulls[0].Waits) != 1 || pulls[0].Waits[0].Resolved != want {
			t.Fatalf("wait resolution must be %t: %+v", want, pulls)
		}
		return detail
	}
	pushReview := func(id int, revision, submitted string) {
		t.Helper()
		body := fmt.Sprintf(`{"action":"submitted","repository":{"id":10},"pull_request":{"number":2},"review":{"id":%d,"state":"approved","commit_id":%q,"submitted_at":%q}}`, id, revision, submitted)
		deliver(fmt.Sprint(id), "pull_request_review", body)
	}
	pushReview(21, "abc", "2026-11-21T03:00:00Z")
	pushReview(22, "old", "2026-11-21T03:01:00Z")
	checkWait(false)
	pushReview(23, "abc", "2026-11-21T03:02:00Z")
	checkWait(true)

	// Then a controller restart retains the reviews and the wait result while the runner stays offline.
	d.stop()
	d = startDex(t, dir, "")
	probeRunner()
	afterRestart := checkWait(true)
	if len(afterRestart.Entries) != 4 || !strings.Contains(afterRestart.Entries[0].Content, "GitHub review: approved") {
		t.Fatalf("accepted review missing after restart: %+v", afterRestart.Entries)
	}

	// A redelivery adds no duplicate and does not change the unrelated Task.
	pushReview(23, "abc", "2026-11-21T03:02:00Z")
	detail := checkWait(true)
	if len(detail.Entries) != 4 || detail.Entries[3].Content != "Fix login" {
		t.Fatalf("unexpected Issue Task timeline: %+v", detail.Entries)
	}
	d.must(t, http.MethodGet, "/api/agents/default/inbox/tasks/"+other.Id, nil, &detail)
	if len(detail.Entries) != 0 {
		t.Fatalf("unrelated Task received reviews: %+v", detail.Entries)
	}
}
