package app_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"uuid"

	"github.com/recally-io/dex/internal/mcptest"

	"github.com/recally-io/dex/internal/app"
)

func TestUserReadsExtensionLevelsAndPrivateEnvironment(t *testing.T) {
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

func TestUserReloadChecksOtherOpenAgentsAndKeepsEnvironmentsSeparate(t *testing.T) {
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
	// When B has either invalid file, reloading A is refused and names B.
	for _, kind := range []string{"mcp.json", ".env"} {
		path := filepath.Join(data, "agents", b.ID, ".agents", kind)
		before, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		writeFile(t, path, "invalid-private-token")
		status, body := d.reload(t)
		if status != http.StatusBadRequest || !strings.Contains(body, "Other agent") || strings.Contains(body, "invalid-private-token") {
			t.Fatalf("reload: %d %s", status, body)
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

func TestUserUpgradesOldAgentExtensionsWithoutOverwrite(t *testing.T) {
	for _, scenario := range []string{"move", "both", "relative", "internal-link", "symlink", "interrupted"} {
		t.Run(scenario, func(t *testing.T) {
			// Given old extension files, with a possible interrupted or conflicting upgrade.
			data, home := t.TempDir(), t.TempDir()
			root := filepath.Join(data, "agents", "default")
			writeSkill(t, filepath.Join(root, "skills"), "old", "description: Old skill")
			writeMCP(t, filepath.Join(root, "mcp.json"), map[string]any{})
			switch scenario {
			case "both":
				writeSkill(t, filepath.Join(root, ".agents", "skills"), "new", "description: New skill")
			case "relative":
				if err := os.Symlink("../../outside", filepath.Join(root, "skills", "link")); err != nil {
					t.Fatal(err)
				}
			case "internal-link":
				if err := os.Symlink("old", filepath.Join(root, "skills", "alias")); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(filepath.Join(root, "skills"), filepath.Join(root, "external")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "external"), filepath.Join(root, "skills")); err != nil {
					t.Fatal(err)
				}
			case "interrupted":
				if err := os.MkdirAll(filepath.Join(root, ".agents"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			d := startDexHome(t, app.Config{DataDir: data}, home)
			// When the worker reads extensions twice, each move happens at most once.
			first := d.extensions(t)
			second := d.extensions(t)
			warn := scenario == "both" || scenario == "relative"
			if (len(first.Warnings) > 0) != warn || len(first.Errors) > 0 || len(second.Warnings) != len(first.Warnings) {
				t.Fatalf("first=%+v second=%+v", first, second)
			}
			_, oldErr := os.Lstat(filepath.Join(root, "skills"))
			if warn && oldErr != nil || !warn && !os.IsNotExist(oldErr) {
				t.Fatalf("old tree: %v", oldErr)
			}
			if _, err := os.Lstat(filepath.Join(root, ".agents", "mcp.json")); err != nil {
				t.Fatal(err)
			}
			if scenario == "symlink" {
				entry, err := os.Lstat(filepath.Join(root, ".agents", "skills"))
				if err != nil || entry.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("symlink: %v %v", entry, err)
				}
			}
			// Then a worker restart also leaves the migration stable.
			d.stop()
			d = startDexHome(t, app.Config{DataDir: data}, home)
			third := d.extensions(t)
			if len(third.Errors) > 0 || (len(third.Warnings) > 0) != warn {
				t.Fatalf("restart: %+v", third)
			}
		})
	}
}
