package app_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/execenv/execenvtest"
)

type apiSessionEnvironment struct {
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

// Given a local bot and an SSH environment, when sessions select environments, then their
// command and file tools run only in the selected environment.
func TestUserRunsSessionsInSelectedEnvironments(t *testing.T) {
	const apiKey = "e2b_session_secret"
	sshFix := execenvtest.NewFakeSSH(t)
	fake := execenvtest.NewFakeE2B(t, apiKey)
	cfg := app.Config{DataDir: t.TempDir(), Home: t.TempDir(), SSHProgram: sshFix.SSHProgram, E2BAPIURL: fake.APIURL(), E2BEnvdURL: fake.EnvdURL()}
	d := startDexWith(t, cfg)

	var remoteEnv, sandbox apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name": "remote-box", "kind": "pi-env", "config": map[string]any{"host": "remote.internal", "user": "dev"},
	}, &remoteEnv, http.StatusCreated)
	d.must(t, http.MethodPost, "/api/settings/environments/"+remoteEnv.ID+"/test", nil, nil)
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name": "sandbox-a", "kind": "e2b", "config": map[string]any{"template": "base", "timeoutSeconds": 300}, "secret": apiKey,
	}, &sandbox, http.StatusCreated)

	// Then the bot lists its own environment first, then the SSH environment. E2B is not a choice.
	var listed struct {
		Environments []apiSessionEnvironment `json:"environments"`
	}
	status, raw := d.call(t, http.MethodGet, "/api/agents/default/environments", nil)
	if status != http.StatusOK || strings.Contains(raw, apiKey) {
		t.Fatalf("list environments: %d %s", status, raw)
	}
	d.must(t, http.MethodGet, "/api/agents/default/environments", nil, &listed)
	if len(listed.Environments) != 2 || listed.Environments[0].ID != "local" || !listed.Environments[0].Default ||
		listed.Environments[1] != (apiSessionEnvironment{ID: remoteEnv.ID, Name: "remote-box", Kind: "pi-env", Status: "available"}) {
		t.Fatalf("environments=%+v", listed.Environments)
	}

	// When a new chat selects remote-box, then bash, write, and deliver_file use the SSH host.
	d.setModel(t, "bash")
	remote := d.newSession(t).ID
	var selected apiChatEnvironment
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/environment", remote), map[string]any{"environmentId": remoteEnv.ID}, &selected)
	if selected.EnvironmentID != remoteEnv.ID {
		t.Fatalf("selected=%+v", selected)
	}
	if out := taskTurn(t, d, remote, `printf %s "${REMOTE_SSH_MARKER:-local}"`); out != "bash: ssh-fake-ok" {
		t.Fatalf("remote bash=%q", out)
	}
	if out := taskTool(t, d, remote, "write", map[string]any{"path": "report.md", "content": "# Remote\n"}); strings.HasPrefix(out, "tool error:") {
		t.Fatalf("remote write=%q", out)
	}
	if delivered := deliverArtifact(t, d, remote, "report.md"); delivered.Name != "report.md" {
		t.Fatalf("delivered=%+v", delivered)
	}
	if _, body := readArtifact(t, d, "default", "report.md"); string(body) != "# Remote\n" {
		t.Fatalf("artifact=%q", body)
	}

	// Then a chat that selects nothing still runs on the bot's local environment.
	d.setModel(t, "bash")
	local := d.newSession(t).ID
	if out := taskTurn(t, d, local, `printf %s "${REMOTE_SSH_MARKER:-local}"`); out != "bash: local" {
		t.Fatalf("local bash=%q", out)
	}

	// A started chat keeps its environment. A missing or an E2B environment is refused.
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/environment", remote), map[string]any{"environmentId": ""}, nil, http.StatusConflict)
	fresh := d.newSession(t).ID
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/environment", fresh), map[string]any{"environmentId": "missing"}, nil, http.StatusBadRequest)
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/environment", fresh), map[string]any{"environmentId": sandbox.ID}, nil, http.StatusBadRequest)

	// When the bot lists environments, then it sees the SSH environment without secrets.
	tool := taskTool(t, d, local, "environment_list", map[string]any{})
	if !strings.Contains(tool, remoteEnv.ID) || strings.Contains(tool, sandbox.ID) || strings.Contains(tool, apiKey) {
		t.Fatalf("environment_list=%q", tool)
	}

	// When the local chat creates a child on remote-box, then the child runs there.
	var child childRef
	toolJSON(t, callSessionTool(t, d, local, "session_new", map[string]any{"title": "Remote", "message": `printf %s "${REMOTE_SSH_MARKER:-local}"`, "provider": "faux", "modelId": "bash", "environment": remoteEnv.ID}), &child)
	results := waitResults(t, sessionDB(t, d), local, 1)
	if !strings.Contains(results[0].content, "ssh-fake-ok") {
		t.Fatalf("child result=%+v", results[0])
	}
	if got := callSessionTool(t, d, local, "session_new", map[string]any{"title": "Sandbox", "message": "hi", "environment": sandbox.ID}); !strings.Contains(got, "SSH environment") {
		t.Fatalf("session_new in E2B=%q", got)
	}

	// After a restart the chat still runs on remote-box.
	d.stop()
	d = startDexWith(t, cfg)
	d.setModel(t, "bash")
	if out := taskTurn(t, d, remote, `printf %s "${REMOTE_SSH_MARKER:-local}"`); out != "bash: ssh-fake-ok" {
		t.Fatalf("bash after restart=%q", out)
	}

	// When the owner deletes remote-box, then the chat on it fails and never runs locally.
	d.must(t, http.MethodDelete, "/api/settings/environments/"+remoteEnv.ID, nil, nil)
	d.setModel(t, "bash")
	if out := taskTurn(t, d, remote, `printf %s "${REMOTE_SSH_MARKER:-local}"`); !strings.Contains(out, "was removed") || out == "bash: local" {
		t.Fatalf("bash after removal=%q", out)
	}
}

// Given sessions that follow the bot's environment, when the owner changes that environment,
// then running background work blocks the change, and after it the sessions drop the old cwd.
// A chat that selects local explicitly stays local.
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

	pinned := d.newSession(t).ID
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/chats/%d/environment", pinned), map[string]any{"environmentId": "local"}, nil)

	// When the command ends, then the change succeeds and the session starts in the new workspace.
	callSessionTool(t, d, chatID, "background_stop", map[string]any{"id": bg.ID})
	d.must(t, http.MethodPut, "/api/agents/default", bot, nil)
	assertProject(t, d, chatID, "")
	d.setModel(t, "bash")
	if out := taskTurn(t, d, chatID, `printf %s "${REMOTE_E2B_MARKER:-local}"`); out != "bash: e2b-fake-ok" {
		t.Fatalf("bash after change=%q", out)
	}
	// Then a chat that selects local explicitly stays local.
	d.setModel(t, "bash")
	if out := taskTurn(t, d, pinned, `printf %s "${REMOTE_E2B_MARKER:-local}"`); out != "bash: local" {
		t.Fatalf("pinned bash after change=%q", out)
	}
}
