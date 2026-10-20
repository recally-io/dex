package engine

import (
	"encoding/json/v2"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/recally-io/dex/internal/mcptest"
)

func extensionFixture(t *testing.T) (*agentExtensions, string, string) {
	t.Helper()
	home, data := t.TempDir(), t.TempDir()
	a := newAgentExtensions(home, data, "a", map[string]string{"BASE": "explicit"})
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
	// Given values and skills at all three levels.
	a, home, data := extensionFixture(t)
	agent := filepath.Join(data, "agents", "a")
	for _, dir := range []string{home, data, agent} {
		extensionFile(t, filepath.Join(dir, ".agents", ".env"), "TOKEN="+filepath.Base(dir)+"\n")
		extensionFile(t, filepath.Join(dir, ".agents", "skills", "notes", "SKILL.md"), "---\ndescription: "+filepath.Base(dir)+"\n---\n")
	}
	loaded, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close(t.Context())
	if loaded.Env["TOKEN"] != "a" || loaded.Env["BASE"] != "explicit" {
		t.Fatalf("environment: %#v", loaded.Env)
	}
	info, err := a.Info(t.Context())
	if err != nil || len(info.Env) != 3 || !info.Env[0].Shadowed || info.Env[2].Shadowed || len(info.Skills) != 1 || !info.Skills[0].Overrides || info.Skills[0].Scope != "agent" {
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
func TestUserEnvEditsPreserveLinesAndRedactValues(t *testing.T) {
	a, _, data := extensionFixture(t)
	path := filepath.Join(data, ".agents", ".env")
	extensionFile(t, path, "# keep\r\nTOKEN=old\r\nTOKEN=duplicate\r\nOTHER=stay\r\n")
	secret := "token # ' quoted"
	info, err := a.EditEnv(t.Context(), "dex", "TOKEN", &secret)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "# keep\r\nTOKEN=\"token # ' quoted\"\r\nOTHER=stay\r\n" {
		t.Fatalf("edited lines: %q", raw)
	}
	values, err := readExtensionEnv(path)
	if err != nil || values["TOKEN"] != secret {
		t.Fatalf("quote roundtrip: %v %v", values, err)
	}
	encoded, err := json.Marshal(info)
	if err != nil || strings.Contains(string(encoded), secret) {
		t.Fatalf("secret leaked: %s %v", encoded, err)
	}
	stat, err := os.Stat(path)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v %v", stat, err)
	}
	if _, err := a.EditEnv(t.Context(), "dex", "TOKEN", nil); err != nil {
		t.Fatal(err)
	}
	values, err = readExtensionEnv(path)
	if err != nil || values["TOKEN"] != "" {
		t.Fatalf("delete: %v %v", values, err)
	}
	before, _ := os.ReadFile(path)
	bad := "private\nKEY=bad"
	if _, err := a.EditEnv(t.Context(), "dex", "TOKEN", &bad); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe value: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("invalid edit changed file")
	}
}
func TestUserSharedEnvEditsDoNotLoseUpdates(t *testing.T) {
	a, home, data := extensionFixture(t)
	other := newAgentExtensions(home, data, "b", nil)
	var wg sync.WaitGroup
	for i, owner := range []*agentExtensions{a, other} {
		wg.Go(func() {
			_, err := owner.EditEnv(t.Context(), "dex", []string{"FIRST", "SECOND"}[i], new("value"))
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	values, err := readExtensionEnv(filepath.Join(data, ".agents", ".env"))
	if err != nil || len(values) != 2 {
		t.Fatalf("shared edits: %v %v", values, err)
	}
}
func TestUserInvalidEnvPreventsMCPAndPartialSecrets(t *testing.T) {
	a, home, data := extensionFixture(t)
	extensionFile(t, filepath.Join(home, ".agents", ".env"), "WIDE=secret\n")
	extensionFile(t, filepath.Join(data, ".agents", ".env"), "NARROW=secret\nbroken-secret-line\n")
	extensionFile(t, filepath.Join(home, ".agents", "mcp.json"), `{"mcpServers":{"wide":{"command":"must-not-run"}}}`)
	loaded, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.HasTools || len(loaded.Servers) != 0 || len(loaded.Env) != 1 || loaded.Env["BASE"] != "explicit" {
		t.Fatalf("invalid configuration applied: %#v", loaded)
	}
	info, _ := a.Info(t.Context())
	encoded, _ := json.Marshal(info)
	if strings.Contains(string(encoded), "secret") {
		t.Fatalf("error leaked secret: %s", encoded)
	}
}
func TestUserMCPConnectsCallsAndRedactsConfiguration(t *testing.T) {
	a, _, data := extensionFixture(t)
	dir := t.TempDir()
	server := httptest.NewServer(&mcptest.Server{Dir: dir, Twice: true})
	defer server.Close()
	config := map[string]any{"mcpServers": map[string]any{"notes": map[string]any{"type": "http", "url": server.URL, "headers": map[string]string{"Authorization": "Bearer ${TOKEN}"}}}}
	encoded, _ := json.Marshal(config)
	extensionFile(t, filepath.Join(data, ".agents", "mcp.json"), string(encoded))
	extensionFile(t, filepath.Join(data, ".agents", ".env"), "TOKEN=private-token\n")
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
	encoded, _ = json.Marshal(info)
	if strings.Contains(string(encoded), "private-token") || strings.Contains(string(encoded), server.URL) {
		t.Fatalf("configuration leaked: %s", encoded)
	}
	t.Logf("artifact: %s", filepath.Join(dir, "notes.txt"))
}
func TestUserStdioMCPGetsOnlyExplicitEnvironment(t *testing.T) {
	if os.Getenv("DEX_NATIVE_MCP_CHILD") == "1" {
		if os.Getenv("PARENT_ONLY_SECRET") != "" {
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
	config := map[string]any{"mcpServers": map[string]any{"local": map[string]any{"command": binary, "args": []string{"-test.run=^TestUserStdioMCPGetsOnlyExplicitEnvironment$"}, "env": map[string]string{"DEX_NATIVE_MCP_CHILD": "1", "MCP_DIR": t.TempDir()}}}}
	encoded, _ := json.Marshal(config)
	extensionFile(t, filepath.Join(data, ".agents", "mcp.json"), string(encoded))
	loaded, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close(t.Context())
	if !loaded.HasTools || loaded.Servers[0].State != "connected" {
		t.Fatalf("explicit environment server: %#v", loaded.Servers)
	}
}

func TestUserMalformedMCPDoesNotRestoreWiderServers(t *testing.T) {
	a, home, data := extensionFixture(t)
	extensionFile(t, filepath.Join(home, ".agents", "mcp.json"), `{"mcpServers":{"notes":{"command":"not-started"}}}`)
	extensionFile(t, filepath.Join(data, ".agents", "mcp.json"), `{"mcpServers":{"notes":{"disabled":true}}}`)
	info, err := a.Info(t.Context())
	if err != nil || len(info.Servers) != 1 || info.Servers[0].State != "disabled" || !info.Servers[0].Overrides {
		t.Fatalf("disabled override: %#v %v", info, err)
	}
	extensionFile(t, filepath.Join(data, ".agents", "mcp.json"), `{"mcpServers":{"notes":{"headers":"private-secret"}}}`)
	loaded, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close(t.Context())
	info, _ = a.Info(t.Context())
	encoded, _ := json.Marshal(info)
	if loaded.HasTools || len(info.Servers) != 0 || len(info.Errors) == 0 || strings.Contains(string(encoded), "private-secret") {
		t.Fatalf("invalid MCP state: %s", encoded)
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
func TestUserEnvQuoteRoundTrip(t *testing.T) {
	a, _, data := extensionFixture(t)
	for _, value := range []string{"", "plain", `a"b`, "a'b", " a#b ", "x#y"} {
		if _, err := a.EditEnv(t.Context(), "dex", "TOKEN", &value); err != nil {
			t.Fatal(err)
		}
		values, err := readExtensionEnv(filepath.Join(data, ".agents", ".env"))
		if err != nil || values["TOKEN"] != value {
			t.Fatalf("quote roundtrip for %q failed", value)
		}
	}
}

func TestUserReloadValidationKeepsTheLoadedEnvironment(t *testing.T) {
	// Given a loaded environment and an MCP file that changes after loading.
	a, _, data := extensionFixture(t)
	path := filepath.Join(data, ".agents", ".env")
	extensionFile(t, path, "TOKEN=first-private-value\n")
	loaded, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close(t.Context())
	for _, name := range []string{"mcp.json", ".env"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(data, ".agents", name)
			before, err := os.ReadFile(path)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			extensionFile(t, path, "invalid-private-value")
			// When reload validation reads the invalid file, it reports only the file and error category.
			err = a.Validate(t.Context())
			validation, ok := errors.AsType[*EnvEditError](err)
			if !ok || !errors.Is(err, ErrInvalidInput) || !strings.Contains(validation.Error(), path) || strings.Contains(validation.Error(), "private-value") {
				t.Fatalf("validation error: %v", err)
			}
			// Then the loaded environment stays intact until a valid reload replaces it.
			if loaded.Env["TOKEN"] != "first-private-value" {
				t.Fatal("validation changed loaded environment")
			}
			if before == nil {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				extensionFile(t, path, string(before))
			}
		})
	}
	if err := a.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
}
