package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mdFixture returns a home directory and a project directory.
func mdFixture(t *testing.T) (home, project string) {
	t.Helper()
	return t.TempDir(), t.TempDir()
}

func read(t *testing.T, path, home, confine string) agentsMD {
	t.Helper()
	return readAgentsMD(path, home, confine)
}

func TestUserAgentsMDMissingFileChangesNothing(t *testing.T) {
	a, _, _ := extensionFixture(t)
	if got := a.instructions("", "own"); got != "own" {
		t.Fatalf("instructions: %q", got)
	}
	if got := a.instructions("", ""); got != "" {
		t.Fatalf("instructions: %q", got)
	}
}

func TestUserAgentsMDPlainFile(t *testing.T) {
	home, project := mdFixture(t)
	extensionFile(t, filepath.Join(project, "AGENTS.md"), "plain rules\n")
	got := read(t, filepath.Join(project, "AGENTS.md"), home, "")
	if got.text != "plain rules" || len(got.warnings) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestUserAgentsMDSymlinkResolvesImportsFromTheRealFile(t *testing.T) {
	home, project := mdFixture(t)
	extensionFile(t, filepath.Join(home, ".agents", "AGENTS.md"), "main\n@rules/style.md\n")
	extensionFile(t, filepath.Join(home, ".agents", "rules", "style.md"), "STYLE\n")
	link := filepath.Join(project, "AGENTS.md")
	if err := os.Symlink(filepath.Join(home, ".agents", "AGENTS.md"), link); err != nil {
		t.Fatal(err)
	}
	got := read(t, link, home, "")
	if !strings.Contains(got.text, "main\n@rules/style.md") || !strings.Contains(got.text, "STYLE") || len(got.warnings) != 0 {
		t.Fatalf("got %+v", got)
	}
	// The line before an imported file names its real path.
	if !strings.Contains(got.text, "Imported file "+filepath.Join(got.path[:strings.LastIndex(got.path, "/")], "rules", "style.md")+":") {
		t.Fatalf("no path line: %q", got.text)
	}
}

func TestUserAgentsMDHomeAndAbsoluteImports(t *testing.T) {
	home, project := mdFixture(t)
	extensionFile(t, filepath.Join(home, "notes", "me.md"), "FROM-HOME\n")
	abs := filepath.Join(project, "abs.md")
	extensionFile(t, abs, "FROM-ABS\n")
	extensionFile(t, filepath.Join(project, "AGENTS.md"), "@~/notes/me.md\ntext @"+abs+"\n")
	got := read(t, filepath.Join(project, "AGENTS.md"), home, "")
	if !strings.Contains(got.text, "FROM-HOME") || !strings.Contains(got.text, "FROM-ABS") {
		t.Fatalf("got %+v", got)
	}
}

func TestUserAgentsMDIgnoresTokensInCodeAndMidWord(t *testing.T) {
	home, project := mdFixture(t)
	extensionFile(t, filepath.Join(project, "secret.md"), "SECRET\n")
	extensionFile(t, filepath.Join(project, "AGENTS.md"), "```\n@secret.md\n```\n~~~sh\n@secret.md\n~~~\nuse `@secret.md` or ``@secret.md`` or mail a@secret.md\n")
	got := read(t, filepath.Join(project, "AGENTS.md"), home, "")
	if strings.Contains(got.text, "SECRET") || len(got.warnings) != 0 {
		t.Fatalf("got %+v", got)
	}
	// A token after the closed fence still imports.
	extensionFile(t, filepath.Join(project, "AGENTS.md"), "```\n@secret.md\n```\n@secret.md\n")
	if got := read(t, filepath.Join(project, "AGENTS.md"), home, ""); !strings.Contains(got.text, "SECRET") {
		t.Fatalf("got %+v", got)
	}
}

func TestUserAgentsMDMissingFileWarnsOnlyForFileLikeTokens(t *testing.T) {
	home, project := mdFixture(t)
	extensionFile(t, filepath.Join(project, "AGENTS.md"), "@name and @docs/gone.txt and @gone.md and @plain\n")
	got := read(t, filepath.Join(project, "AGENTS.md"), home, "")
	if len(got.warnings) != 2 || !strings.Contains(got.warnings[0], "@docs/gone.txt") || !strings.Contains(got.warnings[1], "@gone.md") {
		t.Fatalf("warnings: %q", got.warnings)
	}
	if !strings.Contains(got.text, "@name and @docs/gone.txt") {
		t.Fatalf("original text changed: %q", got.text)
	}
}

func TestUserAgentsMDCycleReadsEachFileOnce(t *testing.T) {
	home, project := mdFixture(t)
	extensionFile(t, filepath.Join(project, "AGENTS.md"), "root\n@a.md\n")
	extensionFile(t, filepath.Join(project, "a.md"), "A\n@b.md\n")
	extensionFile(t, filepath.Join(project, "b.md"), "B\n@AGENTS.md\n@a.md\n")
	got := read(t, filepath.Join(project, "AGENTS.md"), home, "")
	if strings.Count(got.text, "root\n") != 1 || strings.Count(got.text, "\nA\n") != 1 || strings.Count(got.text, "\nB\n") != 1 || len(got.warnings) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestUserAgentsMDDepthLimit(t *testing.T) {
	home, project := mdFixture(t)
	// AGENTS.md is depth 0; f1 to f5 load; f6 is deeper than the limit.
	extensionFile(t, filepath.Join(project, "AGENTS.md"), "root\n@f1.md\n")
	for i := 1; i <= 6; i++ {
		extensionFile(t, filepath.Join(project, "f"+string(rune('0'+i))+".md"), "FILE"+string(rune('0'+i))+"\n@f"+string(rune('0'+i+1))+".md\n")
	}
	got := read(t, filepath.Join(project, "AGENTS.md"), home, "")
	if !strings.Contains(got.text, "FILE5\n") || strings.Contains(got.text, "FILE6\n") {
		t.Fatalf("depth: %q", got.text)
	}
	if len(got.warnings) != 1 || !strings.Contains(got.warnings[0], "deeper than 5") {
		t.Fatalf("warnings: %q", got.warnings)
	}
}

func TestUserProjectAgentsMDImportsStayInsideTheProject(t *testing.T) {
	home, project := mdFixture(t)
	outside := t.TempDir()
	extensionFile(t, filepath.Join(outside, "out.md"), "OUTSIDE\n")
	extensionFile(t, filepath.Join(project, "in.md"), "INSIDE\n@"+filepath.Join(outside, "out.md")+"\n")
	if err := os.Symlink(outside, filepath.Join(project, "link")); err != nil {
		t.Fatal(err)
	}
	extensionFile(t, filepath.Join(project, "AGENTS.md"), "@in.md\n@~/x.md\n@link/out.md\n")
	extensionFile(t, filepath.Join(home, "x.md"), "HOME\n")
	root := canonicalExtensionPath(project)
	got := read(t, filepath.Join(project, "AGENTS.md"), home, root)
	if !strings.Contains(got.text, "INSIDE") || strings.Contains(got.text, "OUTSIDE") || strings.Contains(got.text, "HOME") {
		t.Fatalf("text: %q", got.text)
	}
	if len(got.warnings) != 3 {
		t.Fatalf("warnings: %q", got.warnings)
	}
	// The agent level has no such limit.
	if got := read(t, filepath.Join(project, "AGENTS.md"), home, ""); !strings.Contains(got.text, "OUTSIDE") || !strings.Contains(got.text, "HOME") {
		t.Fatalf("unconfined: %q", got.text)
	}
	// A project AGENTS.md that links outside the project is skipped.
	linked := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "out.md"), filepath.Join(linked, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(linked, "AGENTS.md"), home, canonicalExtensionPath(linked)); got.text != "" || len(got.warnings) != 1 {
		t.Fatalf("outside main file: %+v", got)
	}
}

func TestUserAgentsMDSourcesAndOrder(t *testing.T) {
	a, home, data := extensionFixture(t)
	project := t.TempDir()
	extensionFile(t, filepath.Join(home, ".agents", "AGENTS.md"), "R-HOME\n")
	extensionFile(t, filepath.Join(project, "AGENTS.md"), "R-PROJECT\n")
	extensionFile(t, filepath.Join(project, "sub", "x"), "")
	// The home file is not read, and a parent of cwd is not read.
	if got := a.instructions(filepath.Join(project, "sub"), ""); got != "" {
		t.Fatalf("instructions: %q", got)
	}
	extensionFile(t, filepath.Join(data, ".agents", "AGENTS.md"), "R-DEX\n")
	extensionFile(t, filepath.Join(data, "agents", "a", ".agents", "AGENTS.md"), "R-AGENT\n")
	got := a.instructions(project, "R-OWN")
	last := -1
	for _, part := range []string{"R-DEX", "R-AGENT", "R-OWN", "R-PROJECT"} {
		at := strings.Index(got, part)
		if at <= last {
			t.Fatalf("%s out of order: %q", part, got)
		}
		last = at
	}
	if strings.Contains(got, "R-HOME") {
		t.Fatalf("home level read: %q", got)
	}
}

func TestUserDexAndAgentImportsHaveNoPathLimit(t *testing.T) {
	a, _, data := extensionFixture(t)
	outside := t.TempDir()
	extensionFile(t, filepath.Join(outside, "o.md"), "OUTSIDE\n")
	extensionFile(t, filepath.Join(data, ".agents", "AGENTS.md"), "@"+filepath.Join(outside, "o.md")+"\n")
	if got := a.instructions("", ""); !strings.Contains(got, "OUTSIDE") {
		t.Fatalf("instructions: %q", got)
	}
}

func TestUserEqualDexAndAgentDirectoriesReadAgentsMDOnce(t *testing.T) {
	home := t.TempDir()
	data := t.TempDir()
	// The agent directory is the dex directory, through a symlink.
	if err := os.MkdirAll(filepath.Join(data, "agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(data, filepath.Join(data, "agents", "a")); err != nil {
		t.Fatal(err)
	}
	extensionFile(t, filepath.Join(data, ".agents", "AGENTS.md"), "ONCE\n")
	a := newAgentExtensions(home, data, "a", newToolEnv(home, nil, nil))
	if got := a.instructions("", ""); strings.Count(got, "ONCE") != 1 {
		t.Fatalf("instructions: %q", got)
	}
}

func TestUserHomeFilesAreNotRead(t *testing.T) {
	a, home, data := extensionFixture(t)
	extensionFile(t, filepath.Join(home, ".agents", ".env"), "HOME_SECRET=x\nbroken line\n")
	extensionFile(t, filepath.Join(home, ".agents", "mcp.json"), `{"mcpServers":{"home":{"command":"must-not-run"}}}`)
	extensionFile(t, filepath.Join(home, ".agents", "skills", "s", "SKILL.md"), "---\ndescription: home\n---\n")
	extensionFile(t, filepath.Join(data, ".agents", ".env"), "DEX_KEY=y\n")
	loaded, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close(t.Context())
	info, _ := a.Info(t.Context())
	if _, ok := loaded.Env["HOME_SECRET"]; ok || loaded.Env["DEX_KEY"] != "y" || len(info.Errors) != 0 || len(info.Env) != 1 || len(info.Servers) != 0 || len(info.Skills) != 0 {
		t.Fatalf("env %v info %+v", loaded.Env, info)
	}
	if len(info.Levels) != 2 || len(info.Warnings) != 0 {
		t.Fatalf("levels %+v warnings %q", info.Levels, info.Warnings)
	}
}
