package app_test

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/mcptest"
)

// These tests use faux/section, which answers with a section of its system prompt, and faux/tool,
// which loads an MCP tool with tool_search, calls it, and answers "tool: <output>". The MCP servers
// are internal/mcptest: this test binary over stdio, or an HTTP test server.

type extensions struct {
	Paths struct {
		GlobalSkills string `json:"globalSkills"`
		AgentMcp     string `json:"agentMcp"`
	} `json:"paths"`
	Skills []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Scope       string `json:"scope"`
		Path        string `json:"path"`
		Overrides   bool   `json:"overrides"`
		Error       string `json:"error"`
	} `json:"skills"`
	Servers []serverInfo `json:"servers"`
	Errors  []string     `json:"errors"`
}

type serverInfo struct {
	Name      string   `json:"name"`
	Scope     string   `json:"scope"`
	Overrides bool     `json:"overrides"`
	Transport string   `json:"transport"`
	State     string   `json:"state"`
	Tools     []string `json:"tools"`
	Error     string   `json:"error"`
}

func (e extensions) server(name string) serverInfo {
	for _, s := range e.Servers {
		if s.Name == name {
			return s
		}
	}
	return serverInfo{}
}

func (d *dex) extensions(t *testing.T) extensions {
	t.Helper()
	var out extensions
	d.must(t, http.MethodGet, "/api/extensions", nil, &out)
	return out
}

// reload reloads the extensions. It tries again while the worker is busy, because a run can hold
// the worker for a moment after its answer.
func (d *dex) reload(t *testing.T) (int, string) {
	t.Helper()
	var status int
	var body string
	eventually(t, "reload", func() (bool, any) {
		status, body = d.call(t, http.MethodPost, "/api/extensions/reload", nil)
		return status != http.StatusConflict, body
	})
	return status, body
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeSkill(t *testing.T, dir, name, frontMatter string) string {
	t.Helper()
	path := filepath.Join(dir, name, "SKILL.md")
	writeFile(t, path, "---\n"+frontMatter+"\n---\n\nSteps of "+name+".\n")
	return path
}

// stdioServer is an mcp.json entry that starts this test binary as an MCP server on dir.
func stdioServer(t *testing.T, dir string) map[string]any {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"command": exe, "env": map[string]string{"DEX_TEST_MCP_DIR": dir}}
}

// httpServer is an mcp.json entry for an MCP server on dir over Streamable HTTP.
func httpServer(t *testing.T, dir string) map[string]any {
	t.Helper()
	srv := httptest.NewServer(&mcptest.Server{Dir: dir})
	t.Cleanup(srv.Close)
	return map[string]any{"type": "http", "url": srv.URL + "/mcp"}
}

func writeMCP(t *testing.T, path string, servers map[string]any) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(b))
}

func readNotes(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "notes.txt"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func TestUserAgentSkillReplacesTheGlobalSkill(t *testing.T) {
	// Given global skills review and notes, and agent skills review and broken
	dataDir, home := t.TempDir(), t.TempDir()
	globalDir := filepath.Join(home, ".agents", "skills")
	agentDir := filepath.Join(dataDir, "agents", app.DefaultAgentID, "skills")
	writeSkill(t, globalDir, "review", "name: review\ndescription: Global review steps")
	notes := writeSkill(t, globalDir, "notes", "description: Take notes")
	review := writeSkill(t, agentDir, "review", "description: Agent review steps")
	writeSkill(t, agentDir, "broken", "name: broken")
	d := startDexHome(t, app.Config{DataDir: dataDir}, home)
	d.setModel(t, "section")

	// When the user opens the extensions
	got := d.extensions(t)

	// Then the agent review replaces the global review, and broken shows its error
	type row struct {
		Name, Scope       string
		Overrides, Broken bool
	}
	var rows []row
	for _, s := range got.Skills {
		rows = append(rows, row{s.Name, s.Scope, s.Overrides, s.Error != ""})
	}
	want := []row{{"broken", "agent", false, true}, {"notes", "global", false, false}, {"review", "agent", true, false}}
	if !slices.Equal(rows, want) || got.Paths.GlobalSkills != globalDir {
		t.Fatalf("skills = %+v, paths = %+v", got.Skills, got.Paths)
	}

	// When the agent reads its skills section
	w := d.watch(t, d.newChat(t, "skills"))

	// Then it lists the agent review and notes with their files, and not the global review or broken
	w.until(t, "answer", answeredWith("section: "))
	answer := w.tr.items[len(w.tr.items)-1].Text
	for _, s := range []string{"- notes: Take notes (" + notes + ")", "- review: Agent review steps (" + review + ")"} {
		if !strings.Contains(answer, s) {
			t.Fatalf("no %q in %q", s, answer)
		}
	}
	if strings.Contains(answer, "Global review") || strings.Contains(answer, "broken") {
		t.Fatalf("answer = %q", answer)
	}
}

func TestUserAgentUsesMCPToolsFromBothScopes(t *testing.T) {
	// Given global servers files (stdio), web (HTTP), shared and old, and agent servers that
	// replace shared, turn off old, and add one whose command does not exist
	dataDir, home := t.TempDir(), t.TempDir()
	filesDir, webDir, globalShared, agentShared := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	writeMCP(t, filepath.Join(home, ".agents", "mcp.json"), map[string]any{
		"files":  stdioServer(t, filesDir),
		"web":    httpServer(t, webDir),
		"shared": stdioServer(t, globalShared),
		"old":    stdioServer(t, t.TempDir()),
	})
	writeMCP(t, filepath.Join(dataDir, "agents", app.DefaultAgentID, "mcp.json"), map[string]any{
		"shared":  stdioServer(t, agentShared),
		"old":     map[string]any{"disabled": true},
		"missing": map[string]any{"command": filepath.Join(home, "no-such-server")},
	})
	d := startDexHome(t, app.Config{DataDir: dataDir}, home)
	d.setModel(t, "tool")

	// When the user opens the extensions before the agent starts
	got := d.extensions(t)

	// Then the servers are idle, and no server has started
	if s := got.server("files"); s.State != "idle" || s.Transport != "stdio" || got.server("old").State != "disabled" {
		t.Fatalf("servers = %+v", got.Servers)
	}
	if _, err := os.Stat(filepath.Join(filesDir, "ops.log")); !os.IsNotExist(err) {
		t.Fatalf("the files server started: %v", err)
	}

	// When the agent calls a tool of each server
	chatID := d.newChat(t, `mcp__files__append {"text":"one"}`)
	w := d.watch(t, chatID)
	w.until(t, "files answer", answered("tool: appended 1"))
	d.send(t, chatID, `mcp__web__append {"text":"two"}`)
	w.until(t, "web answer", answered("tool: appended 1"))
	d.send(t, chatID, `mcp__shared__append {"text":"three"}`)
	w.until(t, "shared answer", answered("tool: appended 1"))
	d.send(t, chatID, `mcp__files__fail {}`)
	w.until(t, "error answer", answered("tool error: failed on purpose"))

	// Then each call reached its server, and the agent server replaced the global one
	if readNotes(t, filesDir) != "one\n" || readNotes(t, webDir) != "two\n" || readNotes(t, agentShared) != "three\n" ||
		readNotes(t, globalShared) != "" {
		t.Fatalf("notes: files %q, web %q, agent shared %q, global shared %q",
			readNotes(t, filesDir), readNotes(t, webDir), readNotes(t, agentShared), readNotes(t, globalShared))
	}
	got = d.extensions(t)
	tools := []string{"append", "fail", "read", "slow_echo", "wait"}
	if s := got.server("files"); s.State != "connected" || !slices.Equal(s.Tools, tools) {
		t.Fatalf("files = %+v", s)
	}
	if s := got.server("shared"); s.Scope != "agent" || !s.Overrides || s.State != "connected" {
		t.Fatalf("shared = %+v", s)
	}
	if s := got.server("missing"); s.State != "failed" || s.Error == "" {
		t.Fatalf("missing = %+v", s)
	}
	if s := got.server("old"); s.State != "disabled" || s.Transport != "" {
		t.Fatalf("old = %+v", s)
	}
}

func TestUserReloadsExtensionsWhenTheAgentIsIdle(t *testing.T) {
	// Given an agent with one MCP server that has answered
	dataDir, home, filesDir := t.TempDir(), t.TempDir(), t.TempDir()
	agentMCP := filepath.Join(dataDir, "agents", app.DefaultAgentID, "mcp.json")
	writeMCP(t, agentMCP, map[string]any{"files": stdioServer(t, filesDir)})
	d := startDexHome(t, app.Config{DataDir: dataDir}, home)
	d.setModel(t, "tool")
	chatID := d.newChat(t, `mcp__files__append {"text":"one"}`)
	w := d.watch(t, chatID)
	w.until(t, "answer", answered("tool: appended 1"))

	// When a call is still running, the user reloads
	d.send(t, chatID, `mcp__files__wait {"name":"slow"}`)
	eventually(t, "started call", func() (bool, any) {
		return strings.Contains(readNotes(t, filesDir), "started slow"), readNotes(t, filesDir)
	})
	status, body := d.call(t, http.MethodPost, "/api/extensions/reload", nil)

	// Then dex refuses, because the agent is busy
	if status != http.StatusConflict {
		t.Fatalf("reload while busy: %d %s", status, body)
	}
	writeFile(t, filepath.Join(filesDir, "slow.release"), "")
	w.until(t, "wait answer", answered("tool: released slow"))

	// When the user adds a skill and a server, then reloads
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "plan", "description: Plan the work")
	moreDir := t.TempDir()
	writeMCP(t, agentMCP, map[string]any{"files": stdioServer(t, filesDir), "more": stdioServer(t, moreDir)})
	var got extensions
	if status, body := d.reload(t); status != http.StatusOK || json.Unmarshal([]byte(body), &got) != nil {
		t.Fatalf("reload: %d %s", status, body)
	}

	// Then the new skill and server are there, and the next run uses the server
	if len(got.Skills) != 1 || got.Skills[0].Name != "plan" || got.server("more").State != "connected" {
		t.Fatalf("after reload: %+v", got)
	}
	d.send(t, chatID, `mcp__more__append {"text":"new"}`)
	w.until(t, "new server answer", answered("tool: appended 1"))

	// When mcp.json is not valid and the user reloads
	writeFile(t, agentMCP, `{"mcpServers": {"files": {"args": []}}}`)
	status, body = d.reload(t)

	// Then dex refuses with the file, and the agent keeps its servers
	if status != http.StatusBadRequest || !strings.Contains(body, agentMCP) {
		t.Fatalf("reload of a file that is not valid: %d %s", status, body)
	}
	if s := d.extensions(t).server("more"); s.State != "connected" {
		t.Fatalf("more = %+v", s)
	}
	d.send(t, chatID, `mcp__files__read {}`)
	w.until(t, "read answer", answered("tool: one\nstarted slow"))
}

func TestInterruptedMCPCallIsNotRepeatedAfterRestart(t *testing.T) {
	// Given an MCP call that is running when dex stops
	dataDir, home, filesDir := t.TempDir(), t.TempDir(), t.TempDir()
	writeMCP(t, filepath.Join(home, ".agents", "mcp.json"), map[string]any{"files": stdioServer(t, filesDir)})
	d := startDexHome(t, app.Config{DataDir: dataDir}, home)
	d.setModel(t, "tool")
	chatID := d.newChat(t, `mcp__files__wait {"name":"crash"}`)
	eventually(t, "started call", func() (bool, any) {
		return strings.Contains(readNotes(t, filesDir), "started crash"), readNotes(t, filesDir)
	})
	d.stop()
	// A call that runs again finishes at once.
	writeFile(t, filepath.Join(filesDir, "crash.release"), "")

	// When dex starts again and the user opens the chat
	d = startDexHome(t, app.Config{DataDir: dataDir}, home)
	w := d.watch(t, chatID)

	// Then the agent gets an error for the call, and the server did not get it again
	w.until(t, "answer", answeredWith("tool error: "))
	if notes := readNotes(t, filesDir); notes != "started crash\n" {
		t.Fatalf("notes = %q", notes)
	}
}

func TestMessageDuringReloadWaitsForTheNewServer(t *testing.T) {
	// Given an agent whose files server has answered
	dataDir, home, oldDir, newDir := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	agentMCP := filepath.Join(dataDir, "agents", app.DefaultAgentID, "mcp.json")
	writeMCP(t, agentMCP, map[string]any{"files": stdioServer(t, oldDir)})
	d := startDexHome(t, app.Config{DataDir: dataDir}, home)
	d.setModel(t, "tool")
	chatID := d.newChat(t, `mcp__files__append {"text":"one"}`)
	w := d.watch(t, chatID)
	w.until(t, "answer", answered("tool: appended 1"))
	d.send(t, chatID, `mcp__files__read {}`)
	w.until(t, "read answer", answered("tool: one"))

	// When files moves to a server that starts only after a file exists, and the user reloads
	slow := stdioServer(t, newDir)
	slow["args"] = []string{"-c", `touch "$DEX_TEST_MCP_DIR/waiting"
while [ ! -f "$DEX_TEST_MCP_DIR/start" ]; do sleep 0.05; done
exec "$0"`, slow["command"].(string)}
	slow["command"] = "/bin/sh"
	slow["env"].(map[string]string)["PATH"] = "/bin:/usr/bin"
	writeMCP(t, agentMCP, map[string]any{"files": slow})
	reloaded := make(chan int, 1)
	go func() {
		for {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+"/api/extensions/reload", nil)
			if err != nil {
				reloaded <- 0
				return
			}
			resp, err := d.srv.Client().Do(req)
			if err != nil {
				reloaded <- 0
				return
			}
			resp.Body.Close()
			// The run can still hold the worker for a moment after its answer.
			if resp.StatusCode != http.StatusConflict {
				reloaded <- resp.StatusCode
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	eventually(t, "slow server start", func() (bool, any) {
		_, err := os.Stat(filepath.Join(newDir, "waiting"))
		return err == nil, err
	})

	// And the user sends a message before the new server answers
	d.send(t, chatID, `mcp__files__append {"text":"two"}`)
	// Without the hold, the run calls the old server in this time.
	time.Sleep(time.Second)
	writeFile(t, filepath.Join(newDir, "start"), "")

	// Then the reload ends, and the message reaches the new server and not the old one
	if status := <-reloaded; status != http.StatusOK {
		t.Fatalf("reload: %d", status)
	}
	w.until(t, "new server answer", answered("tool: appended 1"))
	if readNotes(t, oldDir) != "one\n" || readNotes(t, newDir) != "two\n" {
		t.Fatalf("notes: old %q, new %q", readNotes(t, oldDir), readNotes(t, newDir))
	}
}

func TestExtensionsDoNotShowSecretsOfTheFiles(t *testing.T) {
	// Given servers that fail with secrets in their URL, headers, arguments, environment and output,
	// and a skill with a secret in front matter that is not valid
	dataDir, home, filesDir := t.TempDir(), t.TempDir(), t.TempDir()
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "SECRET-BODY", http.StatusInternalServerError)
	}))
	t.Cleanup(denied.Close)
	writeMCP(t, filepath.Join(home, ".agents", "mcp.json"), map[string]any{
		"files":  stdioServer(t, filesDir),
		"badurl": map[string]any{"type": "http", "url": "SECRET-URL"},
		"denied": map[string]any{
			"type": "http", "url": denied.URL + "/mcp?token=SECRET-QUERY",
			"headers": map[string]string{"Authorization": "Bearer SECRET-HEADER"},
		},
		"crash": map[string]any{
			"command": "/bin/sh", "args": []string{"-c", `echo "$TOKEN $0" >&2; exit 1`, "SECRET-ARG"},
			"env": map[string]string{"TOKEN": "SECRET-ENV"},
		},
	})
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "leaky", "description: [SECRET-YAML")
	d := startDexHome(t, app.Config{DataDir: dataDir}, home)
	d.setModel(t, "tool")

	// When the agent starts, and the user opens and reloads the extensions
	w := d.watch(t, d.newChat(t, `mcp__files__append {"text":"one"}`))
	w.until(t, "answer", answered("tool: appended 1"))
	_, opened := d.call(t, http.MethodGet, "/api/extensions", nil)
	status, reloaded := d.reload(t)

	// Then each failure has a category, and no response has a secret
	got := d.extensions(t)
	for name, want := range map[string]string{
		"badurl": "the URL is not valid",
		"denied": "HTTP status 500",
		"crash":  "the connection failed",
	} {
		if s := got.server(name); s.State != "failed" || !strings.Contains(s.Error, want) {
			t.Errorf("%s = %+v, want an error with %q", name, s, want)
		}
	}
	if got.server("files").State != "connected" || len(got.Skills) != 1 || got.Skills[0].Error == "" {
		t.Fatalf("extensions = %+v", got)
	}
	if status != http.StatusOK {
		t.Fatalf("reload: %d %s", status, reloaded)
	}
	for _, body := range []string{opened, reloaded} {
		if strings.Contains(body, "SECRET") {
			t.Fatalf("a response has a secret: %s", body)
		}
	}

	// When the agent mcp.json is not valid JSON and the user reloads
	agentMCP := filepath.Join(dataDir, "agents", app.DefaultAgentID, "mcp.json")
	writeFile(t, agentMCP, `{"mcpServers": {"x": SECRET-JSON}}`)
	status, body := d.reload(t)

	// Then dex refuses without the text of the file
	if status != http.StatusBadRequest || !strings.Contains(body, agentMCP) || strings.Contains(body, "SECRET") {
		t.Fatalf("reload: %d %s", status, body)
	}
}

func TestFirstServerKeepsAToolNameThatTwoServersHave(t *testing.T) {
	// Given server "a" with tool "b__append", and server "a__b" with tool "append": both are
	// mcp__a__b__append
	dataDir, home, aDir, abDir := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	a := stdioServer(t, aDir)
	a["env"].(map[string]string)["DEX_TEST_MCP_PREFIX"] = "b__"
	writeMCP(t, filepath.Join(home, ".agents", "mcp.json"), map[string]any{"a": a, "a__b": stdioServer(t, abDir)})
	d := startDexHome(t, app.Config{DataDir: dataDir}, home)
	d.setModel(t, "tool")

	// When the agent calls the tool
	w := d.watch(t, d.newChat(t, `mcp__a__b__append {"text":"one"}`))

	// Then server "a" gets the call, and server "a__b" shows the tools that it lost
	w.until(t, "answer", answered("tool: appended 1"))
	if readNotes(t, aDir) != "one\n" || readNotes(t, abDir) != "" {
		t.Fatalf("notes: a %q, a__b %q", readNotes(t, aDir), readNotes(t, abDir))
	}
	got := d.extensions(t)
	if s := got.server("a__b"); s.State != "connected" || !strings.Contains(s.Error, "mcp__a__b__append") {
		t.Fatalf("a__b = %+v", s)
	}
	if s := got.server("a"); s.Error != "" {
		t.Fatalf("a = %+v", s)
	}
}
