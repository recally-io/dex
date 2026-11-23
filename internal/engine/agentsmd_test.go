package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mdEngineFixture(t *testing.T) (home, data, project string) {
	t.Helper()
	return t.TempDir(), t.TempDir(), t.TempDir()
}

func submitInstructions(t *testing.T, home, data, cwd, own string) (string, ExtensionInfo) {
	t.Helper()
	a := coreEngine(t, Config{Home: home, DataDir: data})
	opts := Options{Model: ModelRef{Provider: "faux", ModelID: "section"}, ThinkingLevel: "off", Cwd: cwd, Instructions: own}
	ref := coreCreate(t, a, "a", opts)
	sub, err := a.Submit(t.Context(), ref, Input{
		RequestID: "inst",
		Content:   "instructions",
		Kind:      "message",
		Origin:    "web",
		Source:    "dex:a/chat/1",
		Options:   opts,
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome := coreWait(t, sub)
	info, err := a.Extensions(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(outcome.Answer, "section: "), info
}

func TestUserAgentsMDMissingFileChangesNothing(t *testing.T) {
	home, data, _ := mdEngineFixture(t)
	if got, _ := submitInstructions(t, home, data, "", "own"); got != "<instructions>\nown\n</instructions>" {
		t.Fatalf("instructions: %q", got)
	}
	if got, _ := submitInstructions(t, home, data, "", ""); got != "" {
		t.Fatalf("instructions: %q", got)
	}
}

func TestUserAgentsMDPlainFile(t *testing.T) {
	home, data, project := mdEngineFixture(t)
	extensionFile(t, filepath.Join(project, "AGENTS.md"), "plain rules\n")
	got, info := submitInstructions(t, home, data, project, "")
	if !strings.Contains(got, "plain rules") || len(info.Warnings) != 0 {
		t.Fatalf("got %q warnings %q", got, info.Warnings)
	}
}

func TestUserAgentsMDSymlinkResolvesImportsFromTheRealFile(t *testing.T) {
	home, data, project := mdEngineFixture(t)
	realDir := filepath.Join(project, "config")
	extensionFile(t, filepath.Join(realDir, "AGENTS.md"), "main\n@rules/style.md\n")
	extensionFile(t, filepath.Join(realDir, "rules", "style.md"), "STYLE\n")
	link := filepath.Join(project, "AGENTS.md")
	if err := os.Symlink(filepath.Join(realDir, "AGENTS.md"), link); err != nil {
		t.Fatal(err)
	}
	realStyle, err := filepath.EvalSymlinks(filepath.Join(realDir, "rules", "style.md"))
	if err != nil {
		t.Fatal(err)
	}
	got, info := submitInstructions(t, home, data, project, "")
	if !strings.Contains(got, "main\n@rules/style.md") || !strings.Contains(got, "STYLE") || len(info.Warnings) != 0 {
		t.Fatalf("got %q warnings %q", got, info.Warnings)
	}
	// The line before an imported file names its real path.
	if !strings.Contains(got, "Imported file "+realStyle+":") {
		t.Fatalf("no path line: %q", got)
	}
}

func TestUserAgentsMDHomeAndAbsoluteImports(t *testing.T) {
	_, data, project := mdEngineFixture(t)
	home := project
	extensionFile(t, filepath.Join(home, "notes", "me.md"), "FROM-HOME\n")
	abs := filepath.Join(project, "abs.md")
	extensionFile(t, abs, "FROM-ABS\n")
	extensionFile(t, filepath.Join(project, "AGENTS.md"), "@~/notes/me.md\ntext @"+abs+"\n")
	got, _ := submitInstructions(t, home, data, project, "")
	if !strings.Contains(got, "FROM-HOME") || !strings.Contains(got, "FROM-ABS") {
		t.Fatalf("got %q", got)
	}
}

func TestUserAgentsMDIgnoresTokensInCodeAndMidWord(t *testing.T) {
	home, data, project := mdEngineFixture(t)
	extensionFile(t, filepath.Join(project, "secret.md"), "SECRET\n")
	extensionFile(t, filepath.Join(project, "AGENTS.md"), "```\n@secret.md\n```\n~~~sh\n@secret.md\n~~~\nuse `@secret.md` or ``@secret.md`` or mail a@secret.md\n")
	got, info := submitInstructions(t, home, data, project, "")
	if strings.Contains(got, "SECRET") || len(info.Warnings) != 0 {
		t.Fatalf("got %q warnings %q", got, info.Warnings)
	}
	// A token after the closed fence still imports.
	extensionFile(t, filepath.Join(project, "AGENTS.md"), "```\n@secret.md\n```\n@secret.md\n")
	if got, _ := submitInstructions(t, home, data, project, ""); !strings.Contains(got, "SECRET") {
		t.Fatalf("got %q", got)
	}
}

func TestUserAgentsMDMissingFileWarnsOnlyForFileLikeTokens(t *testing.T) {
	home, data, _ := mdEngineFixture(t)
	ws := filepath.Join(data, "agents", "a", "workspace")
	extensionFile(t, filepath.Join(ws, "AGENTS.md"), "@name and @docs/gone.txt and @gone.md and @plain\n")
	got, info := submitInstructions(t, home, data, "", "")
	if len(info.Warnings) != 2 || !strings.Contains(info.Warnings[0], "@docs/gone.txt") || !strings.Contains(info.Warnings[1], "@gone.md") {
		t.Fatalf("warnings: %q", info.Warnings)
	}
	if !strings.Contains(got, "@name and @docs/gone.txt") {
		t.Fatalf("original text changed: %q", got)
	}
}

func TestUserAgentsMDCycleReadsEachFileOnce(t *testing.T) {
	home, data, _ := mdEngineFixture(t)
	ws := filepath.Join(data, "agents", "a", "workspace")
	extensionFile(t, filepath.Join(ws, "AGENTS.md"), "root\n@a.md\n")
	extensionFile(t, filepath.Join(ws, "a.md"), "A\n@b.md\n")
	extensionFile(t, filepath.Join(ws, "b.md"), "B\n@AGENTS.md\n@a.md\n")
	got, info := submitInstructions(t, home, data, "", "")
	if strings.Count(got, "root\n") != 1 || strings.Count(got, "\nA\n") != 1 || strings.Count(got, "\nB\n") != 1 || len(info.Warnings) != 0 {
		t.Fatalf("got %q warnings %q", got, info.Warnings)
	}
}

func TestUserAgentsMDDepthLimit(t *testing.T) {
	home, data, _ := mdEngineFixture(t)
	// AGENTS.md is depth 0; f1 to f5 load; f6 is deeper than the limit.
	ws := filepath.Join(data, "agents", "a", "workspace")
	extensionFile(t, filepath.Join(ws, "AGENTS.md"), "root\n@f1.md\n")
	for i := 1; i <= 6; i++ {
		extensionFile(t, filepath.Join(ws, "f"+string(rune('0'+i))+".md"), "FILE"+string(rune('0'+i))+"\n@f"+string(rune('0'+i+1))+".md\n")
	}
	got, info := submitInstructions(t, home, data, "", "")
	if !strings.Contains(got, "FILE5\n") || strings.Contains(got, "FILE6\n") {
		t.Fatalf("depth: %q", got)
	}
	if len(info.Warnings) != 1 || !strings.Contains(info.Warnings[0], "deeper than 5") {
		t.Fatalf("warnings: %q", info.Warnings)
	}
}

func TestUserProjectAgentsMDImportsStayInsideTheProject(t *testing.T) {
	home, data, project := mdEngineFixture(t)
	outside := t.TempDir()
	extensionFile(t, filepath.Join(outside, "out.md"), "OUTSIDE\n")
	extensionFile(t, filepath.Join(project, "in.md"), "INSIDE\n@"+filepath.Join(outside, "out.md")+"\n")
	if err := os.Symlink(outside, filepath.Join(project, "link")); err != nil {
		t.Fatal(err)
	}
	extensionFile(t, filepath.Join(project, "AGENTS.md"), "@in.md\n@~/x.md\n@link/out.md\n")
	extensionFile(t, filepath.Join(home, "x.md"), "HOME\n")
	got, _ := submitInstructions(t, home, data, project, "")
	if !strings.Contains(got, "INSIDE") || strings.Contains(got, "OUTSIDE") || strings.Contains(got, "HOME") {
		t.Fatalf("text: %q", got)
	}
	// A workspace warning when scanning its AGENTS.md also flags the three outside imports.
	ws := filepath.Join(data, "agents", "a", "workspace")
	extensionFile(t, filepath.Join(ws, "in.md"), "INSIDE\n@"+filepath.Join(outside, "out.md")+"\n")
	if err := os.Symlink(outside, filepath.Join(ws, "link")); err != nil {
		t.Fatal(err)
	}
	extensionFile(t, filepath.Join(ws, "AGENTS.md"), "@in.md\n@~/x.md\n@link/out.md\n")
	_, info := submitInstructions(t, home, data, "", "")
	if len(info.Warnings) != 3 {
		t.Fatalf("warnings: %q", info.Warnings)
	}
	// A project AGENTS.md that links outside the project is skipped.
	linked := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "out.md"), filepath.Join(linked, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if got, _ := submitInstructions(t, home, t.TempDir(), linked, ""); got != "" {
		t.Fatalf("outside main file: %q", got)
	}
}

func TestUserAgentsMDSourcesAndOrder(t *testing.T) {
	home, data, project := mdEngineFixture(t)
	extensionFile(t, filepath.Join(home, ".agents", "AGENTS.md"), "R-HOME\n")
	extensionFile(t, filepath.Join(project, "AGENTS.md"), "R-PROJECT\n")
	extensionFile(t, filepath.Join(project, "sub", "x"), "")
	// The home file is not read, and a parent of cwd is not read.
	if got, _ := submitInstructions(t, home, data, filepath.Join(project, "sub"), ""); got != "" {
		t.Fatalf("instructions: %q", got)
	}
	extensionFile(t, filepath.Join(data, ".agents", "AGENTS.md"), "R-DEX\n")
	extensionFile(t, filepath.Join(data, "agents", "a", ".agents", "AGENTS.md"), "R-AGENT\n")
	got, _ := submitInstructions(t, home, data, project, "R-OWN")
	last := -1
	for _, part := range []string{"R-OWN", "R-PROJECT"} {
		at := strings.Index(got, part)
		if at <= last {
			t.Fatalf("%s out of order: %q", part, got)
		}
		last = at
	}
	for _, part := range []string{"R-HOME", "R-DEX", "R-AGENT"} {
		if strings.Contains(got, part) {
			t.Fatalf("unexpected %s in instructions: %q", part, got)
		}
	}
}
