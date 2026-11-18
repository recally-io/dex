package execenv_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	"github.com/recally-io/dex/internal/execenv"
	"github.com/recally-io/dex/internal/execenv/execenvtest"
)

func TestUserLocalProviderManagesWorkspaceAndEnv(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	homeDir := t.TempDir()
	provider := execenv.NewLocalProvider(dataDir, homeDir)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })

	h, err := provider.Open(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close(context.Background()) }()

	if h.Kind() != execenv.KindLocal {
		t.Fatalf("unexpected handle metadata: kind=%q", h.Kind())
	}
	wantWS := filepath.Join(dataDir, "agents", "default", "workspace")
	if h.Workspace() != wantWS {
		t.Fatalf("workspace=%q want=%q", h.Workspace(), wantWS)
	}
	info, err := os.Stat(wantWS)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("workspace stat err=%v mode=%v", err, info.Mode())
	}
	home, err := h.Home(t.Context())
	if err != nil || home != homeDir {
		t.Fatalf("home=%q err=%v want=%q", home, err, homeDir)
	}
	env, err := h.Env(t.Context(), "", map[string]string{"DEX_FROM_HANDLE": "h1"})
	if err != nil {
		t.Fatal(err)
	}
	var stdout strings.Builder
	res, err := env.Exec(t.Context(), `printf "%s|%s" "$DEX_TEST_LOCAL" "${DEX_FROM_HANDLE:-}"`, &durableenv.ShellExecOptions{
		InheritEnv: new(false),
		Env:        map[string]string{"DEX_TEST_LOCAL": "1", "PATH": os.Getenv("PATH")},
		OnOutput: func(_ context.Context, text string, _ durableenv.ShellOutputInfo) {
			stdout.WriteString(text)
		},
	})
	if err != nil || res.ExitCode != 0 || stdout.String() != "1|" {
		t.Fatalf("local exec res=%+v err=%v out=%q", res, err, stdout.String())
	}
}

func TestUserWorkspaceHelpersStageReadSyncAndListDirectories(t *testing.T) {
	t.Parallel()
	remoteHome := t.TempDir()
	remoteRoot := t.TempDir()
	localWS := t.TempDir()
	provider := execenvtest.NewProvider(execenv.KindPiEnv, remoteRoot, remoteHome, nil)

	h, err := provider.Open(t.Context(), "bot1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close(context.Background()) }()
	env, err := h.Env(t.Context(), "", nil)
	if err != nil {
		t.Fatal(err)
	}

	// StageUpload + ReadWorkspaceFile + symlink/traversal confinement.
	uploadFile := filepath.Join(localWS, "uploads", "u1", "note.txt")
	if err := os.MkdirAll(filepath.Dir(uploadFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(uploadFile, []byte("hello remote upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := execenv.StageUpload(t.Context(), env, localWS, h.Workspace(), "uploads/u1/note.txt"); err != nil {
		t.Fatal(err)
	}
	data, err := execenv.ReadWorkspaceFile(t.Context(), env, h.Workspace(), "uploads/u1/note.txt", 1024)
	if err != nil || string(data) != "hello remote upload" {
		t.Fatalf("read staged upload=%q err=%v", data, err)
	}

	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(h.Workspace(), "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := execenv.ReadWorkspaceFile(t.Context(), env, h.Workspace(), "link.txt", 1024); err == nil {
		t.Fatal("expected final symlink rejection")
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(h.Workspace(), "dirlink")); err != nil {
		t.Fatal(err)
	}
	if _, err := execenv.ReadWorkspaceFile(t.Context(), env, h.Workspace(), "dirlink/secret.txt", 1024); err == nil {
		t.Fatal("expected parent dir symlink escape rejection")
	}
	if _, err := execenv.ReadWorkspaceFile(t.Context(), env, h.Workspace(), "uploads/u1/note.txt", 5); err == nil {
		t.Fatal("expected maxBytes rejection")
	}
	if _, err := execenv.ReadWorkspaceFile(t.Context(), env, h.Workspace(), "../outside", 1024); err == nil {
		t.Fatal("expected path traversal rejection")
	}

	// SyncSkills copies regular skill files and skips .env, mcp.json, dotfiles, node_modules, and symlinks.
	levelDir := filepath.Join(t.TempDir(), ".agents")
	skillsDir := filepath.Join(levelDir, "skills")
	for p, body := range map[string]string{
		filepath.Join(levelDir, ".env"):                         "SECRET=level\n",
		filepath.Join(levelDir, "mcp.json"):                     `{"mcpServers":{}}`,
		filepath.Join(skillsDir, "deploy", "SKILL.md"):          "---\nname: deploy\ndescription: Deploy\n---\nRun deploy\n",
		filepath.Join(skillsDir, "deploy", "scripts", "run.sh"): "#!/bin/sh\necho ok\n",
		filepath.Join(skillsDir, "deploy", ".env"):              "SECRET=nested\n",
		filepath.Join(skillsDir, "deploy", "mcp.json"):          `{"mcpServers":{"bad":{}}}`,
		filepath.Join(skillsDir, ".hidden", "SKILL.md"):         "hidden",
		filepath.Join(skillsDir, "node_modules", "pkg", "a.js"): "ignored",
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(skillsDir, "deploy", "leak.txt")); err != nil {
		t.Fatal(err)
	}
	remoteSkills := filepath.Join(h.Workspace(), ".dex", "skills", "agent")
	if err := env.WriteFile(t.Context(), filepath.Join(remoteSkills, "stale", "SKILL.md"), "stale"); err != nil {
		t.Fatal(err)
	}
	if err := execenv.SyncSkills(t.Context(), env, levelDir, remoteSkills); err != nil {
		t.Fatal(err)
	}
	gotSkill, err := env.ReadTextFile(t.Context(), filepath.Join(remoteSkills, "deploy", "SKILL.md"))
	if err != nil || !strings.Contains(gotSkill, "Run deploy") {
		t.Fatalf("SKILL.md=%q err=%v", gotSkill, err)
	}
	for _, forbidden := range []string{
		filepath.Join(remoteSkills, ".env"),
		filepath.Join(remoteSkills, "mcp.json"),
		filepath.Join(remoteSkills, "deploy", ".env"),
		filepath.Join(remoteSkills, "deploy", "mcp.json"),
		filepath.Join(remoteSkills, "deploy", "leak.txt"),
		filepath.Join(remoteSkills, ".hidden", "SKILL.md"),
		filepath.Join(remoteSkills, "node_modules", "pkg", "a.js"),
		filepath.Join(remoteSkills, "stale", "SKILL.md"),
	} {
		if exists, err := env.Exists(t.Context(), forbidden); err != nil || exists {
			t.Fatalf("forbidden file copied/kept: %s (err=%v)", forbidden, err)
		}
	}

	// ListDirectories & ResolveProjectPath.
	for _, dir := range []string{"Zulu", "alpha/.git", "Beta", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(remoteHome, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(remoteHome, "Beta", ".git"), []byte("gitdir: elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteHome, "file"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(remoteHome, "Zulu"), filepath.Join(remoteHome, "linked")); err != nil {
		t.Fatal(err)
	}
	tilde := "~/./"
	listing, err := execenv.ListDirectories(t.Context(), env, remoteHome, &tilde)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range listing.Entries {
		names = append(names, entry.Name)
		if entry.Git != (entry.Name == "alpha" || entry.Name == "Beta") {
			t.Fatalf("unexpected git flag for %+v", entry)
		}
	}
	if !slices.Equal(names, []string{"alpha", "Beta", "linked", "Zulu"}) || listing.Path != remoteHome || listing.Home != remoteHome {
		t.Fatalf("unexpected remote listing: %+v", listing)
	}
	for _, bad := range []string{"relative", filepath.Join(remoteHome, "missing"), filepath.Join(remoteHome, "file")} {
		if _, err := execenv.ResolveProjectPath(t.Context(), env, remoteHome, bad); !errors.Is(err, execenv.ErrInvalidPath) {
			t.Fatalf("bad path %q err=%v", bad, err)
		}
	}
}
