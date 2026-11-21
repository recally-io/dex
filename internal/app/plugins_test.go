package app_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/api"
)

func TestUserTurnsOffGitHubAndKeepsItsTaskLinks(t *testing.T) {
	t.Parallel()
	// Given a Task that waits for a review of a GitHub PR, made while the GitHub plugin was on.
	dataDir := t.TempDir()
	d := startDex(t, dataDir, "")
	var health api.Health
	d.must(t, http.MethodGet, "/api/health", nil, &health)
	if !slices.Equal(health.Plugins, []string{"github"}) {
		t.Fatalf("health must list the GitHub plugin: %+v", health.Plugins)
	}
	secret := strings.Repeat("s", 40)
	d.must(t, http.MethodPut, "/api/plugins/github/webhooks/10", map[string]string{"secret": secret}, nil, http.StatusNoContent)
	var task api.WorkTask
	d.must(t, http.MethodPost, "/api/agents/default/inbox/tasks", map[string]string{"title": "Ship PR 2"}, &task, http.StatusCreated)
	d.must(t, http.MethodPut, "/api/plugins/github/agents/default/tasks/"+task.Id+"/pull", map[string]any{"repositoryId": 10, "pullNumber": 2, "wait": map[string]any{"revision": "abc", "afterAt": 1}}, nil, http.StatusNoContent)

	// When the owner turns GitHub off, it keeps running until dex starts again.
	d.must(t, http.MethodPut, "/api/settings/plugins/github", map[string]bool{"enabled": false}, nil, http.StatusNoContent)
	d.must(t, http.MethodPut, "/api/settings/plugins/unknown", map[string]bool{"enabled": true}, nil, http.StatusNotFound)
	var plugins api.ListPlugins200JSONResponse
	d.must(t, http.MethodGet, "/api/settings/plugins", nil, &plugins)
	if !slices.Equal(plugins.Plugins, []api.PluginSetting{{Name: "github", Enabled: false, Running: true}}) {
		t.Fatalf("the choice must wait for a restart: %+v", plugins.Plugins)
	}
	d.stop()
	d = startDex(t, dataDir, "")

	// Then GitHub has no hook, API, tool, or UI.
	d.must(t, http.MethodGet, "/api/health", nil, &health)
	if len(health.Plugins) != 0 {
		t.Fatalf("health must list no plugins: %+v", health.Plugins)
	}
	deliverGitHubStatus(t, d, secret, "review-1", "pull_request_review", `{}`, http.StatusNotFound)
	d.must(t, http.MethodPut, "/api/plugins/github/webhooks/10", map[string]string{"secret": secret}, nil, http.StatusNotFound)
	var main chatInfo
	d.must(t, http.MethodGet, "/api/agents/default/chats/main", nil, &main)
	got := taskTool(t, d, main.ID, "task_link_github_pull", map[string]any{"id": task.Id, "repositoryId": 10, "pullNumber": 3})
	t.Logf("artifact: tool call=%s", got)
	if !strings.Contains(got, "Tool task_link_github_pull is not available") {
		t.Fatalf("the GitHub tool must be gone: %s", got)
	}

	// And the Task still shows the PR link and its wait as stored.
	var detail api.GetWorkTask200JSONResponse
	d.must(t, http.MethodGet, "/api/agents/default/inbox/tasks/"+task.Id, nil, &detail)
	t.Logf("artifact: links=%+v", detail.Links)
	if len(detail.Links) != 1 || detail.Links[0].Namespace != "github.com/repository/10" || detail.Links[0].Type != "pull_request" || detail.Links[0].Key != "2" || len(detail.Links[0].Waits) != 1 || detail.Links[0].Waits[0].Revision != "abc" {
		t.Fatalf("the Task must keep its GitHub link: %+v", detail.Links)
	}
}
