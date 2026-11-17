package app_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/execenv/execenvtest"
)

func TestUserManagesE2BSandboxResetsAndSwitchesEnvironments(t *testing.T) {
	t.Setenv("HOST_E2B_SECRET", "host-secret-must-not-leak")
	fake := execenvtest.NewFakeE2B(t, "e2b_live_secret")
	dataDir := t.TempDir()

	d := startDexWith(t, app.Config{
		DataDir:    dataDir,
		E2BAPIURL:  fake.APIURL(),
		E2BEnvdURL: fake.EnvdURL(),
		LoginEnv: func(context.Context, map[string]string) (map[string]string, error) {
			return map[string]string{"PATH": os.Getenv("PATH"), "LOGIN_E2B_SECRET": "login-must-not-leak"}, nil
		},
	})

	// Create E2B environment without leaking write-only secret in response.
	const apiKey = "e2b_live_secret"
	status, rawCreate := d.call(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name":   "Cloud Sandbox",
		"kind":   "e2b",
		"config": map[string]any{"template": "base", "timeoutSeconds": 300},
		"secret": apiKey,
	})
	if status != http.StatusCreated || strings.Contains(rawCreate, apiKey) {
		t.Fatalf("create e2b status=%d body=%s", status, rawCreate)
	}
	var envs struct {
		Environments []apiEnvironment `json:"environments"`
	}
	d.must(t, http.MethodGet, "/api/settings/environments", nil, &envs)
	if len(envs.Environments) != 1 || !envs.Environments[0].SecretSet {
		t.Fatalf("environments=%+v", envs.Environments)
	}
	envID := envs.Environments[0].ID

	// Probe environment succeeds.
	var probe apiEnvironmentTestResult
	d.must(t, http.MethodPost, "/api/settings/environments/"+envID+"/test", nil, &probe, http.StatusOK)
	if !probe.OK || probe.Home != "/home/user" {
		t.Fatalf("probe=%+v", probe)
	}

	// Resetting a local bot fails with 400.
	d.must(t, http.MethodPost, "/api/agents/default/environment/reset", nil, nil, http.StatusBadRequest)

	// Bind default bot to E2B and execute bash, write/read/deliver_file, and memory_* tools.
	var defAgent apiAgent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &defAgent)
	defAgent.Provider = "faux"
	defAgent.ModelID = "bash"
	defAgent.ThinkingLevel = "off"
	defAgent.EnvironmentID = envID
	d.must(t, http.MethodPut, "/api/agents/default", defAgent, &defAgent)
	d.must(t, http.MethodPost, "/api/agents/default/extensions/reload", nil, nil)

	chatID := d.newSession(t).ID
	out := taskTurn(t, d, chatID, `printf "%s|%s|%s" "$REMOTE_E2B_MARKER" "${HOST_E2B_SECRET:-}" "${LOGIN_E2B_SECRET:-}"`)
	if out != "bash: e2b-fake-ok||" {
		t.Fatalf("unexpected e2b bash output: %q", out)
	}

	_ = taskTool(t, d, chatID, "write", map[string]any{"path": "report.md", "content": "# E2B Report\n"})
	delivered := deliverArtifact(t, d, chatID, "report.md")
	if delivered.Name != "report.md" {
		t.Fatalf("delivered=%+v", delivered)
	}
	_, artBody := readArtifact(t, d, "default", "report.md")
	if string(artBody) != "# E2B Report\n" {
		t.Fatalf("artifact=%q", artBody)
	}

	memRes := taskTool(t, d, chatID, "memory_write", map[string]any{"path": "projects/e2b.md", "content": "# E2B Memory\n- durable fact\n"})
	if strings.HasPrefix(memRes, "tool error:") {
		t.Fatalf("memory_write failed: %s", memRes)
	}

	// Stateless reset kills current sandbox; next message starts a fresh sandbox where report.md is gone.
	eventually(t, "chat settled before reset", func() (bool, any) {
		var info chatInfo
		d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", chatID), nil, &info)
		return !info.Busy, info.Busy
	})
	killsBefore := fake.Counts().Killed
	d.must(t, http.MethodPost, "/api/agents/default/environment/reset", nil, nil, http.StatusOK)
	if fake.Counts().Killed <= killsBefore {
		t.Fatalf("expected sandbox kill on reset, counts=%+v", fake.Counts())
	}
	missingRead := taskTool(t, d, chatID, "read", map[string]any{"path": "report.md"})
	if !strings.HasPrefix(missingRead, "tool error:") {
		t.Fatalf("expected fresh sandbox without old report.md, got %q", missingRead)
	}

	// Switching bot back to local cleans up the E2B sandbox automatically.
	killsBeforeSwitch := fake.Counts().Killed
	d.must(t, http.MethodGet, "/api/agents/default", nil, &defAgent)
	defAgent.EnvironmentID = ""
	d.must(t, http.MethodPut, "/api/agents/default", defAgent, &defAgent)
	if fake.Counts().Killed <= killsBeforeSwitch {
		t.Fatalf("expected sandbox cleanup on environment switch, counts=%+v", fake.Counts())
	}
}
