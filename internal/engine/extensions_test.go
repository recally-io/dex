package engine

import (
	"encoding/json/v2"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	envnode "github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/recally-io/dex/internal/mcptest"
)

func extensionFixture(t *testing.T) (*agentExtensions, string, string) {
	t.Helper()
	home, data := t.TempDir(), t.TempDir()
	a := newAgentExtensions(home, data, "a", nil, newToolEnv(home, map[string]string{"BASE": "explicit"}, nil))
	return a, home, data
}
func extensionFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestUserExtensionsMergeAndRefreshSkills(t *testing.T) {
	// Given skills at dex and agent levels.
	a, _, data := extensionFixture(t)
	agent := filepath.Join(data, "agents", "a")
	for _, dir := range []string{data, agent} {
		extensionFile(t, filepath.Join(dir, ".agents", "skills", "notes", "SKILL.md"), "---\ndescription: "+filepath.Base(dir)+"\n---\n")
	}
	loaded, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close(t.Context())
	if loaded.Env["BASE"] != "explicit" {
		t.Fatalf("environment: %#v", loaded.Env)
	}
	info, err := a.Info(t.Context())
	if err != nil || len(info.Skills) != 1 || !info.Skills[0].Overrides || info.Skills[0].Scope != "agent" {
		t.Fatalf("merge: %#v %v", info, err)
	}
	// When the skill changes, the next request sees the new description.
	extensionFile(t, filepath.Join(agent, ".agents", "skills", "notes", "SKILL.md"), "---\ndescription: refreshed\n---\n")
	prompt, err := loaded.Extension.Sections[1].Render(t.Context(), durable.PromptInput{})
	if err != nil || prompt == nil || !strings.Contains(*prompt, "refreshed") {
		t.Fatalf("skill prompt: %v %v", prompt, err)
	}
	extensionFile(t, filepath.Join(agent, ".agents", "skills", "notes", "SKILL.md"), "broken")
	prompt, err = loaded.Extension.Sections[1].Render(t.Context(), durable.PromptInput{})
	if err != nil || prompt != nil {
		t.Fatalf("broken skill exposed wider entry: %v %v", prompt, err)
	}
}
func TestUserProjectSkillsFollowTheConversationCwd(t *testing.T) {
	// Given a project skill in the bot workspace and another in a repository
	a, _, data := extensionFixture(t)
	repo := t.TempDir()
	extensionFile(t, filepath.Join(data, "agents", "a", "workspace", ".agents", "skills", "notes", "SKILL.md"), "---\ndescription: workspace notes\n---\n")
	extensionFile(t, filepath.Join(repo, ".agents", "skills", "tools", "lint", "SKILL.md"), "---\ndescription: repo lint\n---\n")
	loaded, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close(t.Context())
	section := loaded.Extension.Sections[1]

	// When a request has no environment, then the project level is the workspace
	prompt, err := section.Render(t.Context(), durable.PromptInput{})
	if err != nil || prompt == nil || !strings.Contains(*prompt, "workspace notes") || strings.Contains(*prompt, "repo lint") {
		t.Fatalf("workspace prompt: %v %v", prompt, err)
	}

	// When the conversation's cwd is the repository, then the project level is the repository
	env := envnode.NewNodeExecutionEnv(envnode.NodeExecutionEnvOptions{Cwd: repo})
	prompt, err = section.Render(t.Context(), durable.PromptInput{Env: env})
	if err != nil || prompt == nil || !strings.Contains(*prompt, "- lint: repo lint") || strings.Contains(*prompt, "workspace notes") {
		t.Fatalf("repository prompt: %v %v", prompt, err)
	}
}
func TestUserMCPConnectsCallsAndRedactsConfiguration(t *testing.T) {
	a, _, data := extensionFixture(t)
	dir := t.TempDir()
	server := httptest.NewServer(&mcptest.Server{Dir: dir, Twice: true})
	defer server.Close()
	a.mcp = testMCPStore(t, data)
	testSetMCP(t, a.mcp, "", "notes", serverConfig{Type: "http", URL: server.URL, Headers: map[string]string{"Authorization": "Bearer ${vault:TOKEN}"}})
	if err := a.vault.Edit("a", "dex", "TOKEN", new("private-token")); err != nil {
		t.Fatal(err)
	}
	loaded, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close(t.Context())
	if !loaded.HasTools || loaded.Servers[0].State != "connected" || len(loaded.Extension.Tools) != 4 {
		t.Fatalf("MCP state: %#v", loaded.Servers)
	}
	index := slices.IndexFunc(loaded.Extension.Tools, func(tool *durable.ToolRegistration) bool { return tool.Name == "mcp__notes__append" })
	if index < 0 {
		t.Fatal("append tool absent")
	}
	result, err := loaded.Extension.Tools[index].Execute(t.Context(), map[string]any{"text": "native MCP"}, nil)
	if err != nil || !strings.Contains(ai.ContentText(result.Output), "appended 1") {
		t.Fatalf("tool result: %#v %v", result, err)
	}
	artifact, err := os.ReadFile(filepath.Join(dir, "notes.txt"))
	if err != nil || string(artifact) != "native MCP\n" {
		t.Fatalf("artifact: %q %v", artifact, err)
	}
	info, _ := a.Info(t.Context())
	encoded, _ := json.Marshal(info)
	if strings.Contains(string(encoded), "private-token") || strings.Contains(string(encoded), server.URL) {
		t.Fatalf("configuration leaked: %s", encoded)
	}
	t.Logf("artifact: %s", filepath.Join(dir, "notes.txt"))
}
func TestUserStdioMCPGetsOnlyExplicitEnvironment(t *testing.T) {
	if os.Getenv("DEX_NATIVE_MCP_CHILD") == "1" {
		if os.Getenv("PARENT_ONLY_SECRET") != "" || os.Getenv("MCP_TOKEN") != "api:stdio-vault-fixture" {
			os.Exit(8)
		}
		server := mcptest.Server{Dir: os.Getenv("MCP_DIR")}
		if err := server.ServeStdio(os.Stdin, os.Stdout); err != nil {
			os.Exit(9)
		}
		os.Exit(0)
	}
	a, _, data := extensionFixture(t)
	t.Setenv("PARENT_ONLY_SECRET", "hidden")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(a.workspace, 0700); err != nil {
		t.Fatal(err)
	}
	a.mcp = testMCPStore(t, data)
	if err := a.vault.Edit("a", "agent", "STDIO_TEST", new("stdio-vault-fixture")); err != nil {
		t.Fatal(err)
	}
	testSetMCP(t, a.mcp, "", "local", serverConfig{Command: binary, Args: []string{"-test.run=^TestUserStdioMCPGetsOnlyExplicitEnvironment$"}, Env: map[string]string{"DEX_NATIVE_MCP_CHILD": "1", "MCP_DIR": t.TempDir(), "MCP_TOKEN": "api:${vault:STDIO_TEST}"}})
	loaded, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close(t.Context())
	if !loaded.HasTools || loaded.Servers[0].State != "connected" {
		t.Fatalf("explicit environment server: %#v", loaded.Servers)
	}
}

func TestUserDisabledMCPOverrideSurvivesInvalidWrite(t *testing.T) {
	a, home, data := extensionFixture(t)
	a.mcp = testMCPStore(t, data)
	testSetMCP(t, a.mcp, "", "notes", serverConfig{Command: "not-started"})
	testSetMCP(t, a.mcp, "a", "notes", serverConfig{Disabled: true})
	info, err := a.Info(t.Context())
	if err != nil || len(info.Servers) != 1 || info.Servers[0].State != "disabled" || !info.Servers[0].Overrides {
		t.Fatalf("disabled override: %#v %v", info, err)
	}
	if err := a.mcp.set(t.Context(), "a", "notes", serverConfig{Type: "http"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid declaration: %v", err)
	}
	info, err = a.Info(t.Context())
	if err != nil || len(info.Servers) != 1 || info.Servers[0].State != "disabled" {
		t.Fatalf("invalid write changed the override: %#v %v", info, err)
	}
	other := newAgentExtensions(home, data, "b", nil, a.env)
	other.mcp = a.mcp
	info, err = other.Info(t.Context())
	if err != nil || len(info.Servers) != 1 || info.Servers[0].Scope != "dex" || info.Servers[0].State != "idle" {
		t.Fatalf("override affected another agent: %#v %v", info, err)
	}
}
func TestUserSkillMetadataMustBeText(t *testing.T) {
	for _, front := range []string{"description: true", "description: 42", "name: 42\ndescription: valid", "description: valid\n---invalid"} {
		t.Run(front, func(t *testing.T) {
			a, _, data := extensionFixture(t)
			extensionFile(t, filepath.Join(data, ".agents", "skills", "notes", "SKILL.md"), "---\n"+front+"\n---\n")
			info, err := a.Info(t.Context())
			if err != nil || len(info.Skills) != 1 || info.Skills[0].Error == "" {
				t.Fatalf("invalid metadata accepted: %#v %v", info.Skills, err)
			}
		})
	}
}
