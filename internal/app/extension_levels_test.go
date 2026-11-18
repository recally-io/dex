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
	t.Parallel()
	// Given dex and agent levels with the same skill and environment key, and home files that dex must ignore.
	data, home := t.TempDir(), t.TempDir()
	for _, root := range []string{data, filepath.Join(data, "agents", "default")} {
		writeSkill(t, filepath.Join(root, ".agents", "skills"), "review", "description: "+root)
	}
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "home-only", "description: from home")
	writeMCP(t, filepath.Join(home, ".agents", "mcp.json"), map[string]any{"home-server": stdioServer(t, t.TempDir())})
	writeFile(t, filepath.Join(home, ".agents", ".env"), "DEX_LEVEL=home\nDEX_HOME_ONLY=must-not-reach-the-agent\n")
	writeFile(t, filepath.Join(data, ".agents", ".env"), "# Shared environment\n\nDEX_LEVEL=\"dex\" # replaced by agent\nDEX_DEX_ONLY=dex#frag # comment\n")
	writeFile(t, filepath.Join(data, "agents", "default", ".agents", ".env"), "export DEX_LEVEL='agent'\nHOME=/does-not-move-roots\n")
	d := startDexHome(t, app.Config{DataDir: data}, home)
	d.setModel(t, "bash")
	// When the agent runs a tool, it gets its own environment snapshot.
	w := d.watch(t, d.newChat(t, `printf '%s/%s/%s' "$DEX_LEVEL" "${DEX_HOME_ONLY-unset}" "$DEX_DEX_ONLY"`))
	w.until(t, "layered environment", answered("bash: agent/unset/dex#frag"))
	// Then the API shows the winning skill, but no values, and no home environment key.
	got := d.extensions(t)
	if len(got.Skills) != 1 || got.Skills[0].Scope != "agent" || !got.Skills[0].Overrides {
		t.Fatalf("skills: %+v", got.Skills)
	}
	if len(got.Servers) != 0 || len(got.Levels) != 2 || got.dir("home") != "" {
		t.Fatalf("home level is visible: %+v %+v", got.Levels, got.Servers)
	}
	var levels []string
	for _, entry := range got.Env {
		if entry.Key == "DEX_LEVEL" {
			levels = append(levels, fmt.Sprintf("%s:%t", entry.Scope, entry.Shadowed))
		}
	}
	if strings.Join(levels, ",") != "dex:true,agent:false" {
		t.Fatalf("env levels: %v", levels)
	}
}

func TestUserUsesHeaderEnvironmentWithoutLeakingValues(t *testing.T) {
	t.Parallel()
	// Given an HTTP server that requires a dex key at the agent level.
	data, home := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(data, ".agents", ".env"), "TOKEN=private-header-token\nNESTED='${TOKEN}'\n")
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
