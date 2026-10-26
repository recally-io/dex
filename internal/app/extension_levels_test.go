package app_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"uuid"

	"github.com/recally-io/dex/internal/mcptest"

	"github.com/recally-io/dex/internal/app"
)

func TestUserReadsExtensionLevelsAndPrivateEnvironment(t *testing.T) {
	t.Parallel()
	// Given three levels with the same skill and environment key.
	data, home := t.TempDir(), t.TempDir()
	for _, root := range []string{home, data, filepath.Join(data, "agents", "default")} {
		writeSkill(t, filepath.Join(root, ".agents", "skills"), "review", "description: "+root)
	}
	writeFile(t, filepath.Join(home, ".agents", ".env"), "DEX_LEVEL=home\nDEX_HOME_ONLY=home#frag # comment\n")
	writeFile(t, filepath.Join(data, ".agents", ".env"), "# Shared environment\n\nDEX_LEVEL=\"dex\" # replaced by agent\n")
	writeFile(t, filepath.Join(data, "agents", "default", ".agents", ".env"), "export DEX_LEVEL='agent'\nHOME=/does-not-move-roots\n")
	d := startDexHome(t, app.Config{DataDir: data}, home)
	d.setModel(t, "bash")
	// When the agent runs a tool, it gets its own environment snapshot.
	w := d.watch(t, d.newChat(t, `printf '%s/%s' "$DEX_LEVEL" "$DEX_HOME_ONLY"`))
	w.until(t, "layered environment", answered("bash: agent/home#frag"))
	// Then the API shows the winning skill, but no values.
	got := d.extensions(t)
	if len(got.Skills) != 1 || got.Skills[0].Scope != "agent" || !got.Skills[0].Overrides {
		t.Fatalf("skills: %+v", got.Skills)
	}
	var levels []string
	for _, entry := range got.Env {
		if entry.Key == "DEX_LEVEL" {
			levels = append(levels, fmt.Sprintf("%s:%t", entry.Scope, entry.Shadowed))
		}
	}
	if strings.Join(levels, ",") != "home:true,dex:true,agent:false" {
		t.Fatalf("env levels: %v", levels)
	}
}

func TestUserUsesDexSkillsAndMCPEnvironment(t *testing.T) {
	t.Parallel()
	// Given a dex skill that replaces a home skill, and inherited server credentials.
	data, home, inherited, explicit := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "notes", "description: Home notes")
	writeSkill(t, filepath.Join(data, ".agents", "skills"), "notes", "description: Dex notes")
	writeFile(t, filepath.Join(home, ".agents", ".env"), "DEX_TEST_MCP_DIR="+inherited+"\n")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	writeMCP(t, filepath.Join(data, "agents", "default", ".agents", "mcp.json"), map[string]any{
		"inherited": map[string]any{"command": exe}, "explicit": stdioServer(t, explicit),
	})
	d := startDexHome(t, app.Config{DataDir: data}, home)
	d.setModel(t, "tool")
	// When both stdio servers receive tool calls.
	w := d.watch(t, d.newChat(t, `mcp__inherited__append {"text":"inherited"}`))
	w.until(t, "inherited env", answered("tool: appended 1"))
	d.send(t, d.mainChat(t), `mcp__explicit__append {"text":"explicit"}`)
	main := d.watch(t, d.mainChat(t))
	main.until(t, "explicit env", answered("tool: appended 1"))
	// Then entry env wins, and the skill comes from dex.
	if readNotes(t, inherited) != "inherited\n" || readNotes(t, explicit) != "explicit\n" {
		t.Fatal("stdio environment precedence failed")
	}
	got := d.extensions(t)
	if len(got.Skills) != 1 || got.Skills[0].Scope != "dex" || !got.Skills[0].Overrides {
		t.Fatalf("skills: %+v", got.Skills)
	}
}

func TestUserGetsNoFallbackFromInvalidExtensionFiles(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"mcp.json", ".env"} {
		t.Run(kind, func(t *testing.T) {
			// Given a valid home server and .env, with an invalid dex configuration.
			data, home := t.TempDir(), t.TempDir()
			writeMCP(t, filepath.Join(home, ".agents", "mcp.json"), map[string]any{"files": stdioServer(t, t.TempDir())})
			writeFile(t, filepath.Join(home, ".agents", ".env"), "DEX_PRIVATE_VALUE=hidden-home-token\n")
			bad := `{"mcpServers":{"bad":{"args":[]}}}`
			if kind == ".env" {
				bad = "DEX_PRIVATE_VALUE=hidden-bad-token\nBAD-KEY=hidden-bad-token"
			}
			writeFile(t, filepath.Join(data, ".agents", kind), bad)
			d := startDexHome(t, app.Config{DataDir: data}, home)
			d.setModel(t, "bash")
			// When the agent opens, no wider MCP server starts.
			command := `printf '%s' "${DEX_PRIVATE_VALUE-unset}"`
			want := "bash: hidden-home-token"
			if kind == ".env" {
				want = "bash: unset"
			}
			w := d.watch(t, d.newChat(t, command))
			w.until(t, "no fallback", answered(want))
			got := d.extensions(t)
			if len(got.Servers) != 0 || len(got.Errors) == 0 {
				t.Fatalf("extensions: %+v", got)
			}
			// Then reload is refused without disclosing values.
			status, body := d.reload(t)
			if status != http.StatusBadRequest || strings.Contains(body, "hidden-") {
				t.Fatalf("reload: %d %s", status, body)
			}
			_, body = d.call(t, http.MethodGet, "/api/agents/default/extensions", nil)
			if strings.Contains(body, "hidden-") {
				t.Fatalf("API disclosed env: %s", body)
			}
		})
	}
}

func TestUserUsesHeaderEnvironmentWithoutLeakingValues(t *testing.T) {
	t.Parallel()
	// Given an HTTP server that requires a home key at the agent level.
	data, home := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(home, ".agents", ".env"), "TOKEN=private-header-token\nNESTED='${TOKEN}'\n")
	mcp := &mcptest.Server{Dir: t.TempDir()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-header-token" || r.Header.Get("X-Literal") != "${TOKEN}" {
			http.Error(w, "bad header", http.StatusUnauthorized)
			return
		}
		mcp.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	writeMCP(t, filepath.Join(data, "agents", "default", ".agents", "mcp.json"), map[string]any{
		"good":     map[string]any{"type": "http", "url": srv.URL, "headers": map[string]string{"Authorization": "Bearer ${TOKEN}", "X-Literal": "${NESTED}"}},
		"missing":  map[string]any{"type": "http", "url": srv.URL, "headers": map[string]string{"Authorization": "${ABSENT_HEADER_KEY}"}},
		"disabled": map[string]any{"type": "http", "url": srv.URL, "disabled": true, "headers": map[string]string{"Authorization": "${ABSENT_HEADER_KEY}"}},
	})
	d := startDexHome(t, app.Config{DataDir: data}, home)
	d.setModel(t, "tool")
	// When the agent calls the valid server, missing keys only fail their own server.
	w := d.watch(t, d.newChat(t, `mcp__good__append {"text":"ok"}`))
	w.until(t, "HTTP header", answered("tool: appended 1"))
	got := d.extensions(t)
	if got.server("good").State != "connected" || got.server("missing").State != "failed" || got.server("disabled").State != "disabled" {
		t.Fatalf("servers: %+v", got.Servers)
	}
	// Then neither API nor errors contain substituted values.
	_, body := d.call(t, http.MethodGet, "/api/agents/default/extensions", nil)
	if strings.Contains(body, "private-header-token") {
		t.Fatal("API disclosed header")
	}
}

func TestUserReloadsOneAgentWithoutChangingOtherEnvironments(t *testing.T) {
	t.Parallel()
	// Given two agents with distinct environments, both open.
	data, home := t.TempDir(), t.TempDir()
	d := startDexHome(t, app.Config{DataDir: data}, home)
	d.setModel(t, "bash")
	var b apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Other agent", "provider": "faux", "modelId": "bash", "thinkingLevel": "off"}, &b, http.StatusCreated)
	for _, id := range []string{"default", b.ID} {
		writeFile(t, filepath.Join(data, "agents", id, ".agents", ".env"), "DEX_ISOLATED="+id+"\n")
		var chat chatInfo
		base := "/api/agents/" + id
		d.must(t, http.MethodGet, base+"/chats/main", nil, &chat)
		path := fmt.Sprintf("%s/chats/%d", base, chat.ID)
		w := d.watchPath(t, path+"/events")
		d.must(t, http.MethodPost, path+"/messages", map[string]string{"id": uuid.NewV7().String(), "text": `printf '%s' "$DEX_ISOLATED"`}, nil, http.StatusAccepted)
		w.until(t, "agent env", answered("bash: "+id))
	}
	// When B has an invalid file, A can reload its own resources.
	for _, kind := range []string{"mcp.json", ".env"} {
		path := filepath.Join(data, "agents", b.ID, ".agents", kind)
		before, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		writeFile(t, path, "invalid-private-token")
		status, body := d.reload(t)
		if status != http.StatusOK || strings.Contains(body, "invalid-private-token") {
			t.Fatalf("unrelated agent blocked reload: %d %s", status, body)
		}
		// Then B refuses its own invalid files and keeps the loaded environment.
		status, body = d.call(t, http.MethodPost, "/api/agents/"+b.ID+"/extensions/reload", nil)
		if status != http.StatusBadRequest || !strings.Contains(body, path) || strings.Contains(body, "invalid-private-token") {
			t.Fatalf("selected agent reload: %d %s", status, body)
		}
		if before == nil {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		} else {
			writeFile(t, path, string(before))
		}
	}
}

func TestUserReadsEqualHomeAndDexOnce(t *testing.T) {
	t.Parallel()
	// Given the data directory is HOME, reached through a symlink.
	home := t.TempDir()
	alias := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "one", "description: One skill")
	writeFile(t, filepath.Join(home, ".agents", ".env"), "ONE=private-once\n")
	before, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	d := startDexHome(t, app.Config{DataDir: alias}, home)
	// When the user reads extensions, the shared directory is dex only.
	got := d.extensions(t)
	if len(got.Skills) != 1 || got.Skills[0].Scope != "dex" || got.Skills[0].Overrides || len(got.Env) != 1 || got.Env[0].Shadowed || got.Env[0].Scope != "dex" {
		t.Fatalf("extensions: %+v", got)
	}
	after, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode() != after.Mode() {
		t.Fatal("HOME mode changed")
	}
}

func TestUserIsToldToMoveOldAgentExtensions(t *testing.T) {
	t.Parallel()
	// Given skills and an mcp.json in the agent directory, where dex read them before the levels.
	data, home := t.TempDir(), t.TempDir()
	root := filepath.Join(data, "agents", "default")
	writeSkill(t, filepath.Join(root, "skills"), "old", "description: Old skill")
	writeMCP(t, filepath.Join(root, "mcp.json"), map[string]any{})
	d := startDexHome(t, app.Config{DataDir: data}, home)
	// When the user opens the extensions, dex warns about each entry with its new place.
	got := d.extensions(t)
	want := []string{
		filepath.Join(root, "skills") + ": dex no longer reads this; move it to " + filepath.Join(root, ".agents", "skills"),
		filepath.Join(root, "mcp.json") + ": dex no longer reads this; move it to " + filepath.Join(root, ".agents", "mcp.json"),
	}
	if !slices.Equal(got.Warnings, want) || len(got.Errors) > 0 || len(got.Skills) > 0 {
		t.Fatalf("extensions = %+v", got)
	}
	// Then dex leaves the files where they are.
	for _, name := range []string{"skills", "mcp.json"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
}
