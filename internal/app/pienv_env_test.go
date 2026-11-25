package app_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/runner/runnertest"
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
	OK             bool     `json:"ok"`
	Platform       string   `json:"platform"`
	Arch           string   `json:"arch"`
	Home           string   `json:"home"`
	Warnings       []string `json:"warnings"`
	HostKeyChanged bool     `json:"hostKeyChanged"`
	Error          string   `json:"error"`
}

func TestUserManagesPiEnvEnvironmentAndExecutesBotRemotely(t *testing.T) {
	t.Setenv("HOST_PIENV_SECRET", "do-not-leak")
	dataDir := t.TempDir()
	localHome := t.TempDir()
	sshFix := runnertest.NewFakeSSH(t)

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

	// Remote background command and remote project path resolution.
	conn := taskResultDB(t, dataDir)
	d.setModel(t, "spawn")
	bgStarted := bgStart(t, d, chatID, map[string]any{"command": `printf "%s\n" "$REMOTE_SSH_MARKER"`})
	if bgStarted.OutputFile == "" {
		t.Fatalf("missing remote background outputFile: %+v", bgStarted)
	}
	wakes := waitWakes(t, conn, chatID, 1)
	if wakes[0].result.Event != "exited" || !strings.Contains(wakes[0].result.Output, "ssh-fake-ok") {
		t.Fatalf("unexpected remote background wake: %+v", wakes[0].result)
	}
	remoteProject := filepath.Join(sshFix.RemoteHome, "remote-proj")
	if projRes := taskTool(t, d, chatID, "set_project", map[string]any{"path": "~/remote-proj"}); strings.HasPrefix(projRes, "tool error:") {
		t.Fatalf("set_project failed: %s", projRes)
	}
	assertProject(t, d, chatID, remoteProject)
	var recent struct {
		Projects []string `json:"projects"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/projects", nil, &recent)
	if !slices.Equal(recent.Projects, []string{remoteProject}) {
		t.Fatalf("recent projects=%v want=[%s]", recent.Projects, remoteProject)
	}

	// Switching bot back to local allows deleting the environment.
	// The turn returns its answer before the chat settles, and a busy chat blocks the switch.
	eventually(t, "chat settled before switch", func() (bool, any) {
		var info chatInfo
		d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", chatID), nil, &info)
		return !info.Busy, info.Busy
	})
	defAgent.EnvironmentID = ""
	d.must(t, http.MethodPut, "/api/agents/default", defAgent, &defAgent)
	d.must(t, http.MethodDelete, "/api/settings/environments/"+created.ID, nil, nil, http.StatusOK)
}

func TestUserDiscoversSSHConfigProbesAndRecoversHostKey(t *testing.T) {
	dataDir := t.TempDir()
	localHome := t.TempDir()
	sshDir := filepath.Join(localHome, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(
		"Host buildbox\n  HostName 10.0.0.5\n  User builder\n  Port 2200\n  ProxyJump gw\n\nHost github.com\n  User git\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "work_ed25519"), []byte("KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "work_ed25519.pub"), []byte("ssh-ed25519 AAA"), 0o600); err != nil {
		t.Fatal(err)
	}

	sshFix := runnertest.NewFakeSSH(t)
	d := startDexHome(t, app.Config{
		DataDir:    dataDir,
		SSHProgram: sshFix.SSHProgram,
	}, localHome)

	// Given local ~/.ssh/config and key files, GET /ssh-config lists concrete hosts and private key filenames.
	var discovery struct {
		Hosts []struct {
			Alias     string `json:"alias"`
			HostName  string `json:"hostName"`
			User      string `json:"user"`
			Port      int    `json:"port"`
			ProxyJump string `json:"proxyJump"`
		} `json:"hosts"`
		IdentityFiles []string `json:"identityFiles"`
	}
	d.must(t, http.MethodGet, "/api/settings/environments/ssh-config", nil, &discovery)
	if len(discovery.Hosts) != 1 || discovery.Hosts[0].Alias != "buildbox" || discovery.Hosts[0].Port != 2200 || discovery.Hosts[0].ProxyJump != "gw" {
		t.Fatalf("unexpected ssh discovery hosts: %+v", discovery.Hosts)
	}
	if len(discovery.IdentityFiles) != 1 || discovery.IdentityFiles[0] != "~/.ssh/work_ed25519" {
		t.Fatalf("unexpected identityFiles: %+v", discovery.IdentityFiles)
	}

	// Pre-save probe verifies connectivity without creating <dataDir>/ssh_known_hosts.
	var preProbe apiEnvironmentTestResult
	d.must(t, http.MethodPost, "/api/settings/environments/probe", map[string]any{
		"kind":   "pi-env",
		"config": map[string]any{"host": "buildbox"},
	}, &preProbe, http.StatusOK)
	if !preProbe.OK || preProbe.Home != sshFix.RemoteHome {
		t.Fatalf("pre-save probe=%+v", preProbe)
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "ssh_known_hosts")); !os.IsNotExist(statErr) {
		t.Fatalf("expected ephemeral pre-save probe not to create ssh_known_hosts, statErr=%v", statErr)
	}

	// Save environment and trust initial host key via test.
	var saved apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name":   "Build Box",
		"kind":   "pi-env",
		"config": map[string]any{"host": "buildbox"},
	}, &saved, http.StatusCreated)

	var firstTest apiEnvironmentTestResult
	d.must(t, http.MethodPost, "/api/settings/environments/"+saved.ID+"/test", nil, &firstTest, http.StatusOK)
	if !firstTest.OK {
		t.Fatalf("firstTest=%+v", firstTest)
	}

	// When remote host key rotates, test and edit-probe both flag hostKeyChanged=true until forgotten.
	sshFix.RotateHostKey(t)
	var rotatedTest apiEnvironmentTestResult
	d.must(t, http.MethodPost, "/api/settings/environments/"+saved.ID+"/test", nil, &rotatedTest, http.StatusOK)
	if rotatedTest.OK || !rotatedTest.HostKeyChanged {
		t.Fatalf("expected HostKeyChanged=true after key rotation, got %+v", rotatedTest)
	}

	var editProbe apiEnvironmentTestResult
	d.must(t, http.MethodPost, "/api/settings/environments/probe", map[string]any{
		"environmentId": saved.ID,
		"kind":          "pi-env",
		"config":        map[string]any{"host": "buildbox"},
	}, &editProbe, http.StatusOK)
	if editProbe.OK || !editProbe.HostKeyChanged {
		t.Fatalf("expected edit probe with unchanged host to detect HostKeyChanged=true, got %+v", editProbe)
	}

	d.must(t, http.MethodPost, "/api/settings/environments/"+saved.ID+"/forget-host-key", nil, nil, http.StatusOK)
	var recoveredTest apiEnvironmentTestResult
	d.must(t, http.MethodPost, "/api/settings/environments/"+saved.ID+"/test", nil, &recoveredTest, http.StatusOK)
	if !recoveredTest.OK || recoveredTest.HostKeyChanged {
		t.Fatalf("expected recovery after forget-host-key, got %+v", recoveredTest)
	}
}
