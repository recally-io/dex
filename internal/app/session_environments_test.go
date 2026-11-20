package app_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/execenv/execenvtest"
)

type apiAllowedEnvironment struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Default bool   `json:"default"`
	Status  string `json:"status"`
}

type apiChatEnvironment struct {
	ID            int64  `json:"id"`
	EnvironmentID string `json:"environmentId"`
}

// Given a local bot with an E2B environment on its allowlist, when sessions select environments,
// then their command and file tools run only in the selected environment.
func TestUserRunsSessionsInSelectedEnvironments(t *testing.T) {
	const apiKey = "e2b_session_secret"
	fake := execenvtest.NewFakeE2B(t, apiKey)
	dataDir := t.TempDir()
	cfg := app.Config{DataDir: dataDir, E2BAPIURL: fake.APIURL(), E2BEnvdURL: fake.EnvdURL()}
	d := startDexWith(t, cfg)

	var created apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name": "sandbox-a", "kind": "e2b", "config": map[string]any{"template": "base", "timeoutSeconds": 300}, "secret": apiKey,
	}, &created, http.StatusCreated)
	envID := created.ID

	// Given the owner allows sandbox-a for the bot.
	status, raw := d.call(t, http.MethodPut, "/api/agents/default/environments", map[string]any{"environmentIds": []string{envID}})
	if status != http.StatusOK || strings.Contains(raw, apiKey) {
		t.Fatalf("set allowlist: %d %s", status, raw)
	}
	var allowed struct {
		Environments []apiAllowedEnvironment `json:"environments"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/environments", nil, &allowed)
	if len(allowed.Environments) != 2 || allowed.Environments[0].ID != "local" || !allowed.Environments[0].Default ||
		allowed.Environments[1] != (apiAllowedEnvironment{ID: envID, Name: "sandbox-a", Kind: "e2b", Status: "available"}) {
		t.Fatalf("allowed=%+v", allowed.Environments)
	}

	// When a new chat selects sandbox-a, then bash, write, and deliver_file use the sandbox.
	d.setModel(t, "bash")
	remote := d.newSession(t).ID
	var selected apiChatEnvironment
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/environment", remote), map[string]any{"environmentId": envID}, &selected)
	if selected.EnvironmentID != envID {
		t.Fatalf("selected=%+v", selected)
	}
	if out := taskTurn(t, d, remote, `printf %s "${REMOTE_E2B_MARKER:-local}"`); out != "bash: e2b-fake-ok" {
		t.Fatalf("remote bash=%q", out)
	}
	if out := taskTool(t, d, remote, "write", map[string]any{"path": "report.md", "content": "# Sandbox\n"}); strings.HasPrefix(out, "tool error:") {
		t.Fatalf("remote write=%q", out)
	}
	if delivered := deliverArtifact(t, d, remote, "report.md"); delivered.Name != "report.md" {
		t.Fatalf("delivered=%+v", delivered)
	}
	if _, body := readArtifact(t, d, "default", "report.md"); string(body) != "# Sandbox\n" {
		t.Fatalf("artifact=%q", body)
	}

	// Then a chat that selects nothing still runs on the bot's local environment.
	d.setModel(t, "bash")
	local := d.newSession(t).ID
	if out := taskTurn(t, d, local, `printf %s "${REMOTE_E2B_MARKER:-local}"`); out != "bash: local" {
		t.Fatalf("local bash=%q", out)
	}

	// A started chat keeps its environment, and an environment off the allowlist is refused.
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/environment", remote), map[string]any{"environmentId": ""}, nil, http.StatusConflict)
	fresh := d.newSession(t).ID
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/environment", fresh), map[string]any{"environmentId": "missing"}, nil, http.StatusBadRequest)

	// When the bot lists environments, then it sees the allowlist without secrets.
	listed := taskTool(t, d, local, "environment_list", map[string]any{})
	if !strings.Contains(listed, envID) || !strings.Contains(listed, "sandbox-a") || strings.Contains(listed, apiKey) {
		t.Fatalf("environment_list=%q", listed)
	}

	// When the local chat creates a child in sandbox-a, then the child runs there.
	var child childRef
	toolJSON(t, callSessionTool(t, d, local, "session_new", map[string]any{"title": "Remote", "message": `printf %s "${REMOTE_E2B_MARKER:-local}"`, "provider": "faux", "modelId": "bash", "environment": envID}), &child)
	results := waitResults(t, sessionDB(t, d), local, 1)
	if !strings.Contains(results[0].content, "e2b-fake-ok") {
		t.Fatalf("child result=%+v", results[0])
	}
	if got := callSessionTool(t, d, local, "session_new", map[string]any{"title": "Lost", "message": "hi", "environment": "missing"}); !strings.Contains(got, "not allowed") || !strings.Contains(got, envID) {
		t.Fatalf("session_new with a missing environment=%q", got)
	}

	// When the owner removes sandbox-a, then the chat on it fails and never runs locally.
	d.must(t, http.MethodPut, "/api/agents/default/environments", map[string]any{"environmentIds": []string{}}, nil)
	d.setModel(t, "bash")
	if out := taskTurn(t, d, remote, `printf %s "${REMOTE_E2B_MARKER:-local}"`); !strings.Contains(out, "not allowed") || strings.Contains(out, "e2b-fake-ok") || out == "bash: local" {
		t.Fatalf("bash after removal=%q", out)
	}

	// When the owner allows sandbox-a again, then the chat runs there again, after a restart too.
	d.must(t, http.MethodPut, "/api/agents/default/environments", map[string]any{"environmentIds": []string{envID}}, nil)
	if out := taskTurn(t, d, remote, `printf %s "${REMOTE_E2B_MARKER:-local}"`); out != "bash: e2b-fake-ok" {
		t.Fatalf("bash after allow=%q", out)
	}
	d.stop()
	d = startDexWith(t, cfg)
	if out := taskTurn(t, d, remote, `printf %s "${REMOTE_E2B_MARKER:-local}"`); out != "bash: e2b-fake-ok" {
		t.Fatalf("bash after restart=%q", out)
	}
}

// Given sessions that follow the bot's environment, when the owner changes that environment,
// then running background work blocks the change, and after it the sessions drop the old cwd.
func TestUserChangesBotEnvironmentForFollowingSessions(t *testing.T) {
	fake := execenvtest.NewFakeE2B(t, "e2b_change_secret")
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), E2BAPIURL: fake.APIURL(), E2BEnvdURL: fake.EnvdURL()})
	var created apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name": "sandbox-b", "kind": "e2b", "config": map[string]any{"template": "base", "timeoutSeconds": 300}, "secret": "e2b_change_secret",
	}, &created, http.StatusCreated)

	chatID := newParent(t, d)
	project := t.TempDir()
	if out := taskTool(t, d, chatID, "set_project", map[string]any{"path": project}); strings.HasPrefix(out, "tool error:") {
		t.Fatalf("set_project=%q", out)
	}
	bg := bgStart(t, d, chatID, map[string]any{"command": "sleep 30", "wake": "none"})

	// When a background command runs in a following session, then the change fails.
	var bot apiAgent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &bot)
	bot.EnvironmentID = created.ID
	d.must(t, http.MethodPut, "/api/agents/default", bot, nil, http.StatusConflict)

	// When the command ends, then the change succeeds and the session starts in the new workspace.
	callSessionTool(t, d, chatID, "background_stop", map[string]any{"id": bg.ID})
	d.must(t, http.MethodPut, "/api/agents/default", bot, nil)
	assertProject(t, d, chatID, "")
	d.setModel(t, "bash")
	if out := taskTurn(t, d, chatID, `printf %s "${REMOTE_E2B_MARKER:-local}"`); out != "bash: e2b-fake-ok" {
		t.Fatalf("bash after change=%q", out)
	}
}
