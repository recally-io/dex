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
	"uuid"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/execenv"
	"github.com/recally-io/dex/internal/execenv/execenvtest"
)

func TestUserExecutesToolsInRemotePipeEnvironment(t *testing.T) {
	t.Setenv("HOST_APP_SECRET", "process-secret-do-not-leak")
	// Given control directories separate from an isolated remote pipe root/home.
	dataDir := t.TempDir()
	remoteHome := t.TempDir()
	remoteRoot := t.TempDir()

	// Populate host-side skills alongside .env and mcp.json (which must never be copied to remote skills).
	dexLevel := filepath.Join(dataDir, ".agents")
	if err := os.MkdirAll(filepath.Join(dexLevel, "skills", "deploy"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dexLevel, "skills", "deploy", "SKILL.md"), "---\nname: deploy\ndescription: Host deploy\n---\nDeploy\n")
	writeFile(t, filepath.Join(dexLevel, ".env"), "EXPLICIT_ENV_KEY=exp-app-val\n")
	writeFile(t, filepath.Join(dexLevel, "mcp.json"), `{"mcpServers":{}}`)

	// Populate a remote project tree under remoteHome.
	remoteProject := filepath.Join(remoteHome, "repo")
	if err := os.MkdirAll(filepath.Join(remoteProject, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(remoteProject, "AGENTS.md"), "REMOTE_AGENTS_MD\n")

	provider := execenvtest.NewProvider(execenv.KindPiEnv, remoteRoot, remoteHome, map[string]string{
		"REMOTE_APP_MARKER": "pipe-app-ok",
	})
	t.Cleanup(func() { _ = provider.Close(context.Background()) })

	d := startDexWith(t, app.Config{
		DataDir: dataDir,
		LoginEnv: func(context.Context, map[string]string) (map[string]string, error) {
			return map[string]string{"PATH": os.Getenv("PATH"), "LOGIN_APP_SECRET": "login-secret-do-not-leak"}, nil
		},
		ExecEnv: provider,
	})

	// When listing directories for default and a second agent over /api/directories?agentId=...
	var second apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{
		"name": "Second", "provider": "faux", "modelId": "echo", "thinkingLevel": "off",
	}, &second, http.StatusCreated)
	for _, agentID := range []string{app.DefaultAgentID, second.ID} {
		var got directoryListing
		d.must(t, http.MethodGet, "/api/directories?agentId="+agentID+"&path=~", nil, &got)
		if got.Home != remoteHome || got.Path != remoteHome {
			t.Fatalf("directories for %s = %+v, want home=%s", agentID, got, remoteHome)
		}
		var names []string
		for _, entry := range got.Entries {
			names = append(names, entry.Name)
			if entry.Name == "repo" && !entry.Git {
				t.Fatalf("expected repo git=true, got %+v", entry)
			}
		}
		if !slices.Contains(names, "repo") {
			t.Fatalf("missing remote repo for %s: %+v", agentID, got.Entries)
		}
	}
	d.must(t, http.MethodGet, "/api/directories?agentId=missing", nil, nil, http.StatusNotFound)

	chatID := d.newSession(t).ID

	// When running bash remotely, host LoginEnv/process secrets are absent while remote daemon & .env vars exist.
	d.setModel(t, "bash")
	bashOut := taskTurn(t, d, chatID, `printf "%s|%s|%s|%s" "$REMOTE_APP_MARKER" "$EXPLICIT_ENV_KEY" "${LOGIN_APP_SECRET:-}" "${HOST_APP_SECRET:-}"`)
	if bashOut != "bash: pipe-app-ok|exp-app-val||" {
		t.Fatalf("unexpected bash env output: %q", bashOut)
	}
	remoteWS := filepath.Join(remoteRoot, "agents", app.DefaultAgentID, "workspace")
	for _, forbidden := range []string{
		filepath.Join(remoteWS, ".dex", "skills", "dex", ".env"),
		filepath.Join(remoteWS, ".dex", "skills", "dex", "mcp.json"),
	} {
		if _, statErr := os.Stat(forbidden); !os.IsNotExist(statErr) {
			t.Fatalf("forbidden file leaked to remote skills: %s (err=%v)", forbidden, statErr)
		}
	}

	// When using write, edit, and read tools on the remote workspace.
	_ = taskTool(t, d, chatID, "write", map[string]any{"path": "reports/summary.md", "content": "# Draft\nline one\n"})
	_ = taskTool(t, d, chatID, "edit", map[string]any{
		"path":  "reports/summary.md",
		"edits": []map[string]any{{"oldText": "# Draft", "newText": "# Final"}},
	})
	readOut := taskTool(t, d, chatID, "read", map[string]any{"path": "reports/summary.md"})
	if !strings.Contains(readOut, "# Final") {
		t.Fatalf("remote read=%q", readOut)
	}

	// When delivering a workspace file with deliver_file, it copies from remote workspace to host artifacts.
	delivered := deliverArtifact(t, d, chatID, "reports/summary.md")
	if delivered.Name != "summary.md" || delivered.Size <= 0 {
		t.Fatalf("unexpected delivered artifact: %+v", delivered)
	}
	_, artifactBody := readArtifact(t, d, app.DefaultAgentID, "summary.md")
	if string(artifactBody) != "# Final\nline one\n" {
		t.Fatalf("artifact body=%q", artifactBody)
	}

	// When uploading an attachment and submitting a message, it stages into the remote workspace.
	up := d.mustUpload(t, "attachment.txt", []byte("REMOTE_APP_UPLOAD"))
	d.setModel(t, "echo")
	w := d.watch(t, chatID)
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/messages", chatID), map[string]any{
		"id": uuid.NewV7().String(), "text": "uploaded", "attachments": []string{up.ID},
	}, nil, http.StatusAccepted)
	w.until(t, "upload echo", answeredWith(filepath.Join(remoteWS, "uploads", up.ID, "attachment.txt")))
	uploadRead := taskTool(t, d, chatID, "read", map[string]any{"path": "uploads/" + up.ID + "/attachment.txt"})
	if !strings.Contains(uploadRead, "REMOTE_APP_UPLOAD") {
		t.Fatalf("remote upload read=%q", uploadRead)
	}

	// When starting a background command remotely, logs go to <remoteWorkspace>/.dex/background/<id>.log and wake delivers tail.
	conn := taskResultDB(t, dataDir)
	d.setModel(t, "spawn")
	bgStarted := bgStart(t, d, chatID, map[string]any{
		"command": `printf "%s|%s|%s\n" "$REMOTE_APP_MARKER" "$EXPLICIT_ENV_KEY" "${HOST_APP_SECRET:-}"`,
	})
	wantLog := filepath.Join(remoteWS, ".dex", "background", bgStarted.ID+".log")
	if bgStarted.OutputFile != wantLog {
		t.Fatalf("background outputFile=%q want=%q", bgStarted.OutputFile, wantLog)
	}
	wakes := waitWakes(t, conn, chatID, 1)
	if wakes[0].result.Event != "exited" || !strings.Contains(wakes[0].result.Output, "pipe-app-ok|exp-app-val|") {
		t.Fatalf("unexpected background wake result: %+v", wakes[0].result)
	}
	bgLog, err := os.ReadFile(wantLog)
	if err != nil || !strings.Contains(string(bgLog), "pipe-app-ok|exp-app-val|") {
		t.Fatalf("remote bg log (%s)=%q err=%v", wantLog, bgLog, err)
	}

	// When calling memory_* tools, host memory is read/updated while cross-agent access fails.
	writeFile(t, filepath.Join(dataDir, "memory", "agents", second.ID, "MEMORY.md"), "OTHER_BOT_SECRET\n")
	memWrite := taskTool(t, d, chatID, "memory_write", map[string]any{"path": "projects/app.md", "content": "# App\n- remote item v1\n"})
	if strings.HasPrefix(memWrite, "tool error:") {
		t.Fatalf("memory_write failed: %s", memWrite)
	}
	_ = taskTool(t, d, chatID, "memory_edit", map[string]any{
		"path":  "projects/app.md",
		"edits": []map[string]any{{"oldText": "v1", "newText": "v2"}},
	})
	memRead := taskTool(t, d, chatID, "memory_read", map[string]any{"path": "projects/app.md"})
	if !strings.Contains(memRead, "remote item v2") {
		t.Fatalf("memory_read=%q", memRead)
	}
	memSearch := taskTool(t, d, chatID, "memory_search", map[string]any{"query": "OTHER_BOT_SECRET"})
	if strings.Contains(memSearch, "OTHER_BOT_SECRET") {
		t.Fatalf("memory_search leaked other agent secret: %q", memSearch)
	}
	crossRead := taskTool(t, d, chatID, "memory_read", map[string]any{"path": "agents/" + second.ID + "/MEMORY.md"})
	if !strings.HasPrefix(crossRead, "tool error:") {
		t.Fatalf("expected cross-agent memory_read refusal, got: %q", crossRead)
	}

	// When calling set_project with remote ~ path, project resolves remotely and updates recent projects.
	projRes := taskTool(t, d, chatID, "set_project", map[string]any{"path": "~/repo"})
	if strings.HasPrefix(projRes, "tool error:") {
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
}
