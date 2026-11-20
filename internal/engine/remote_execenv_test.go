package engine

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/execenv"
	"github.com/recally-io/dex/internal/execenv/execenvtest"
)

func TestUserExecutesToolsInRemotePipeEnvironment(t *testing.T) {
	t.Setenv("HOST_PROCESS_SECRET", "process-secret-do-not-leak")
	// Given control data/home dirs separate from an isolated remote pipe root/home.
	dataDir := t.TempDir()
	hostHome := t.TempDir()
	remoteHome := t.TempDir()
	remoteRoot := t.TempDir()

	// Populate host-side skills (including .env and mcp.json that must never be synced remotely).
	dexLevel := filepath.Join(dataDir, ".agents")
	agentLevel := filepath.Join(dataDir, "agents", "a", ".agents")
	hostFiles := map[string]string{
		filepath.Join(dexLevel, "skills", "deploy", "SKILL.md"): "---\nname: deploy\ndescription: Host deploy skill\n---\nDeploy steps\n",
		filepath.Join(dexLevel, ".env"):                         "EXPLICIT_DOTENV=from-dex-dotenv\n",
		filepath.Join(dexLevel, "mcp.json"):                     `{"mcpServers":{}}`,
		filepath.Join(agentLevel, ".env"):                       "AGENT_DOTENV=from-agent-dotenv\n",
	}
	for p, body := range hostFiles {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Populate a remote project tree outside workspace, with AGENTS.md (+ @import) and project skill.
	remoteProject := filepath.Join(remoteHome, "repo")
	remoteProjectFiles := map[string]string{
		filepath.Join(remoteProject, "AGENTS.md"):                               "Remote project instructions\n@notes/rule.md\n@../escaped.md\n",
		filepath.Join(remoteProject, "notes", "rule.md"):                        "Imported remote rule",
		filepath.Join(remoteHome, "escaped.md"):                                 "MUST_NOT_IMPORT_OUTSIDE",
		filepath.Join(remoteProject, ".agents", "skills", "remote", "SKILL.md"): "---\nname: remote\ndescription: Remote project skill\n---\nRemote instructions\n",
	}
	for p, body := range remoteProjectFiles {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	provider := execenvtest.NewProvider(execenv.KindPiEnv, remoteRoot, remoteHome, map[string]string{
		"REMOTE_DAEMON_VAR": "remote-pipe-ready",
	})
	t.Cleanup(func() { _ = provider.Close(context.Background()) })

	a := coreEngine(t, Config{
		DataDir: dataDir,
		Home:    hostHome,
		BaseEnv: map[string]string{"PATH": os.Getenv("PATH"), "BASE_HOST_VAR": "host-base-secret"},
		LoginEnv: func(context.Context, map[string]string) (map[string]string, error) {
			return map[string]string{"PATH": os.Getenv("PATH"), "LOGIN_HOST_SECRET": "host-login-secret"}, nil
		},
		ExecEnv: provider,
	})

	ref := coreCreate(t, a, "a", coreOptions("echo"))
	remoteWS := filepath.Join(remoteRoot, "agents", "a", "workspace")

	// When a bash command runs remotely, it inherits remote daemon env + explicit .env keys ONLY.
	bashOut := coreWait(t, coreSubmit(t, a, ref, "req-bash", `printf "%s|%s|%s|%s|%s|%s" "$REMOTE_DAEMON_VAR" "$EXPLICIT_DOTENV" "$AGENT_DOTENV" "${LOGIN_HOST_SECRET:-}" "${BASE_HOST_VAR:-}" "${HOST_PROCESS_SECRET:-}"`, "bash")).Answer
	if bashOut != "bash: remote-pipe-ready|from-dex-dotenv|from-agent-dotenv|||" {
		t.Fatalf("unexpected remote env in bash: %q", bashOut)
	}

	// Verify .env and mcp.json were NEVER copied into remote skills.
	for _, forbidden := range []string{
		filepath.Join(remoteWS, ".dex", "skills", "dex", ".env"),
		filepath.Join(remoteWS, ".dex", "skills", "dex", "mcp.json"),
		filepath.Join(remoteWS, ".dex", "skills", "agent", ".env"),
	} {
		if _, statErr := os.Stat(forbidden); !os.IsNotExist(statErr) {
			t.Fatalf("forbidden file present in remote skills: %s (err=%v)", forbidden, statErr)
		}
	}

	// When checking skills prompt section at remote project cwd, synced dex skill uses remote path and remote project skill appears.
	projOpts := Options{Model: ModelRef{Provider: "faux", ModelID: "section"}, ThinkingLevel: "off", Cwd: remoteProject}
	projRef := coreCreate(t, a, "a", projOpts)
	skillsSub, err := a.Submit(t.Context(), projRef, Input{
		RequestID: "req-sec-skills",
		Content:   "skills",
		Kind:      "message",
		Origin:    "web",
		Source:    "dex:a/chat/2",
		Options:   projOpts,
	})
	if err != nil {
		t.Fatal(err)
	}
	skillsSec := coreWait(t, skillsSub).Answer
	wantDexSkillPath := filepath.Join(remoteWS, ".dex", "skills", "dex", "deploy", "SKILL.md")
	canonProject, err := filepath.EvalSymlinks(remoteProject)
	if err != nil {
		canonProject = remoteProject
	}
	wantProjectSkillPath := filepath.Join(canonProject, ".agents", "skills", "remote", "SKILL.md")
	if !strings.Contains(skillsSec, wantDexSkillPath) || strings.Contains(skillsSec, dataDir) {
		t.Fatalf("skills section must map host skill to remote path (%s) without leaking dataDir: %s", wantDexSkillPath, skillsSec)
	}
	if !strings.Contains(skillsSec, wantProjectSkillPath) {
		t.Fatalf("skills section missing remote project skill %s: %s", wantProjectSkillPath, skillsSec)
	}

	// Verify remote project AGENTS.md & confined import loaded while outside import was skipped.
	instSub, err := a.Submit(t.Context(), projRef, Input{
		RequestID: "req-agentsmd",
		Content:   "instructions",
		Kind:      "message",
		Origin:    "web",
		Source:    "dex:a/chat/2",
		Options:   projOpts,
	})
	if err != nil {
		t.Fatal(err)
	}
	inst := coreWait(t, instSub).Answer
	if !strings.Contains(inst, "Remote project instructions") || !strings.Contains(inst, "Imported remote rule") || strings.Contains(inst, "MUST_NOT_IMPORT_OUTSIDE") {
		t.Fatalf("unexpected remote AGENTS.md instructions: %q", inst)
	}

	// When staging an upload from local workspace, submitting delivers it to the remote workspace cleanly.
	localUpload := filepath.Join(dataDir, "agents", "a", "workspace", "uploads", "u1", "note.txt")
	if err := os.MkdirAll(filepath.Dir(localUpload), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localUpload, []byte("REMOTE_UPLOAD_PAYLOAD"), 0o600); err != nil {
		t.Fatal(err)
	}
	uploadSub, err := a.Submit(t.Context(), ref, Input{
		RequestID: "req-upload",
		Content:   "check upload",
		Kind:      "message",
		Origin:    "web",
		Source:    "dex:a/chat/1",
		Attachments: []Attachment{{
			ID:   "u1",
			Name: "note.txt",
			Path: "uploads/u1/note.txt",
			Size: int64(len("REMOTE_UPLOAD_PAYLOAD")),
		}},
		Options: coreOptions("echo"),
	})
	if err != nil {
		t.Fatal(err)
	}
	uploadEcho := coreWait(t, uploadSub).Answer
	wantRemoteUpload := filepath.Join(remoteWS, "uploads", "u1", "note.txt")
	if !strings.Contains(uploadEcho, wantRemoteUpload) {
		t.Fatalf("expected attachment path %s in submitted prompt, got: %q", wantRemoteUpload, uploadEcho)
	}
	uploadReadArgs, _ := json.Marshal(map[string]any{"path": wantRemoteUpload})
	uploadOut := coreWait(t, coreSubmit(t, a, ref, "req-upload-read", "read "+string(uploadReadArgs), "tool")).Answer
	if !strings.Contains(uploadOut, "REMOTE_UPLOAD_PAYLOAD") {
		t.Fatalf("expected upload file readable remotely, got: %q", uploadOut)
	}

	// When writing, editing, and reading a remote workspace file via tools, and reading via Engine helper.
	writeArgs, _ := json.Marshal(map[string]any{"path": "sub/artifact.txt", "content": "hello v1\n"})
	_ = coreWait(t, coreSubmit(t, a, ref, "req-write", "write "+string(writeArgs), "tool"))
	editArgs, _ := json.Marshal(map[string]any{"path": "sub/artifact.txt", "edits": []map[string]any{{"oldText": "v1", "newText": "v2"}}})
	_ = coreWait(t, coreSubmit(t, a, ref, "req-edit", "edit "+string(editArgs), "tool"))
	readOut := coreWait(t, coreSubmit(t, a, ref, "req-read", `read {"path":"sub/artifact.txt"}`, "tool")).Answer
	if !strings.Contains(readOut, "hello v2") {
		t.Fatalf("remote read=%q", readOut)
	}
	artifactBytes, err := a.ReadWorkspaceFile(t.Context(), TaskContext{AgentID: "a"}, "sub/artifact.txt", 1024)
	if err != nil || string(artifactBytes) != "hello v2\n" {
		t.Fatalf("ReadWorkspaceFile=%q err=%v", artifactBytes, err)
	}

	// Verify memory_* tools operate against host memory while execution runs remotely.
	memWriteArgs, _ := json.Marshal(map[string]any{"path": "projects/pipe.md", "content": "# Pipe\n- remote note\n"})
	memRes := coreWait(t, coreSubmit(t, a, ref, "req-mem-write", "memory_write "+string(memWriteArgs), "tool")).Answer
	if !strings.Contains(memRes, "projects/pipe.md") {
		t.Fatalf("memory_write=%q", memRes)
	}
	memSearch := coreWait(t, coreSubmit(t, a, ref, "req-mem-search", `memory_search {"query":"remote note"}`, "tool")).Answer
	if !strings.Contains(memSearch, "projects/pipe.md:2:") {
		t.Fatalf("memory_search=%q", memSearch)
	}

	// Verify ResolveProjectPath and ListDirectories route through the remote pipe handle.
	resolvedProj, err := a.ResolveProjectPath(t.Context(), "a", "", "~/repo")
	if err != nil || resolvedProj != remoteProject {
		t.Fatalf("ResolveProjectPath=%q err=%v want=%q", resolvedProj, err, remoteProject)
	}
	listing, err := a.ListDirectories(t.Context(), "a", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range listing.Entries {
		names = append(names, e.Name)
	}
	if !slices.Contains(names, "repo") || listing.Home != remoteHome {
		t.Fatalf("remote ListDirectories=%+v", listing)
	}
}
