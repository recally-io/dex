package app_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/execenv/execenvtest"
)

type apiEnvironmentConfig struct {
	Host         string `json:"host,omitempty"`
	User         string `json:"user,omitempty"`
	Port         *int   `json:"port,omitempty"`
	IdentityFile string `json:"identityFile,omitempty"`
	WorkspaceDir string `json:"workspaceDir,omitempty"`
}

type apiEnvironment struct {
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	Kind      string               `json:"kind"`
	Config    apiEnvironmentConfig `json:"config"`
	SecretSet bool                 `json:"secretSet"`
	CreatedAt int64                `json:"createdAt"`
	UpdatedAt int64                `json:"updatedAt"`
}

type apiEnvironmentTestResult struct {
	OK       bool     `json:"ok"`
	Platform string   `json:"platform"`
	Arch     string   `json:"arch"`
	Home     string   `json:"home"`
	Warnings []string `json:"warnings"`
	Error    string   `json:"error"`
}

func TestUserManagesPiEnvEnvironmentAndExecutesBotRemotely(t *testing.T) {
	t.Setenv("HOST_PIENV_SECRET", "do-not-leak")
	dataDir := t.TempDir()
	localHome := t.TempDir()
	sshFix := execenvtest.NewFakeSSH(t, dataDir)

	if err := os.MkdirAll(filepath.Join(sshFix.RemoteHome, "remote-proj", ".git"), 0o700); err != nil {
		t.Fatal(err)
	}

	d := startDexWith(t, app.Config{
		DataDir:    dataDir,
		Home:       localHome,
		SSHProgram: sshFix.SSHProgram,
		LoginEnv: func(context.Context, map[string]string) (map[string]string, error) {
			return map[string]string{"PATH": os.Getenv("PATH"), "LOGIN_PIENV_SECRET": "login-do-not-leak"}, nil
		},
	})

	// When creating and testing a pi-env SSH environment via Settings API.
	var created apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name":   "Remote Box",
		"kind":   "pi-env",
		"config": map[string]any{"host": "remote.internal", "user": "dev"},
	}, &created, http.StatusCreated)
	if created.Kind != "pi-env" || created.Config.Host != "remote.internal" {
		t.Fatalf("unexpected created environment: %+v", created)
	}

	var probe apiEnvironmentTestResult
	d.must(t, http.MethodPost, "/api/settings/environments/"+created.ID+"/test", nil, &probe, http.StatusOK)
	if !probe.OK || probe.Home != sshFix.RemoteHome {
		t.Fatalf("unexpected probe: %+v", probe)
	}

	// Binding the default bot to the pi-env environment runs tools remotely and blocks deletion while in use (409).
	var defAgent apiAgent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &defAgent)
	defAgent.Provider = "faux"
	defAgent.ModelID = "bash"
	defAgent.ThinkingLevel = "off"
	defAgent.EnvironmentID = created.ID
	d.must(t, http.MethodPut, "/api/agents/default", defAgent, &defAgent)
	d.must(t, http.MethodPost, "/api/agents/default/extensions/reload", nil, nil)

	d.must(t, http.MethodDelete, "/api/settings/environments/"+created.ID, nil, nil, http.StatusConflict)

	var dirs directoryListing
	d.must(t, http.MethodGet, "/api/directories?agentId=default&path=~", nil, &dirs)
	if dirs.Home != sshFix.RemoteHome {
		t.Fatalf("directories home=%q want=%q", dirs.Home, sshFix.RemoteHome)
	}

	chatID := d.newSession(t).ID
	out := taskTurn(t, d, chatID, `printf "%s|%s|%s" "$REMOTE_SSH_MARKER" "${HOST_PIENV_SECRET:-}" "${LOGIN_PIENV_SECRET:-}"`)
	if out != "bash: ssh-fake-ok||" {
		t.Fatalf("unexpected remote bash output: %q", out)
	}

	_ = taskTool(t, d, chatID, "write", map[string]any{"path": "hello.txt", "content": "from-pienv\n"})
	delivered := deliverArtifact(t, d, chatID, "hello.txt")
	if delivered.Name != "hello.txt" {
		t.Fatalf("delivered=%+v", delivered)
	}
	_, body := readArtifact(t, d, "default", "hello.txt")
	if !strings.Contains(string(body), "from-pienv") {
		t.Fatalf("artifact body=%q", body)
	}

	// Switching bot back to local allows deleting the environment.
	defAgent.EnvironmentID = ""
	d.must(t, http.MethodPut, "/api/agents/default", defAgent, &defAgent)
	d.must(t, http.MethodDelete, "/api/settings/environments/"+created.ID, nil, nil, http.StatusOK)
}
