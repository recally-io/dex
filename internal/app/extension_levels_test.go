package app_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/mcptest"
	"github.com/recally-io/dex/internal/sysops"
)

func TestUserLegacyDotEnvIsIgnoredAndWarnedOnce(t *testing.T) {
	data, home := t.TempDir(), t.TempDir()
	path := filepath.Join(data, "agents", "default", ".agents", ".env")
	writeFile(t, path, "DEX_LEVEL=legacy-private-value\n")
	writeSkill(t, filepath.Join(data, ".agents", "skills"), "review", "description: shared")
	writeSkill(t, filepath.Join(filepath.Dir(path), "skills"), "review", "description: bot")
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "home-only", "description: from home")
	logs := sysops.NewLogBuffer(100)
	old := slog.Default()
	slog.SetDefault(slog.New(sysops.TeeHandler(slog.DiscardHandler, logs)))
	t.Cleanup(func() { slog.SetDefault(old) })
	d := startDexHome(t, app.Config{DataDir: data, Logs: logs}, home)
	d.setModel(t, "bash")
	w := d.watch(t, d.newChat(t, `printf '%s' "${DEX_LEVEL-unset}"`))
	w.until(t, "ignored environment", answered("bash: unset"))
	if status, body := d.reload(t); status != http.StatusOK || strings.Contains(body, "legacy-private-value") {
		t.Fatalf("reload: %d %s", status, body)
	}
	got := d.extensions(t)
	if len(got.Skills) != 1 || got.Skills[0].Scope != "agent" || !got.Skills[0].Overrides || len(got.Levels) != 2 || got.dir("home") != "" {
		t.Fatalf("skill levels: %+v", got)
	}
	var recent api.SystemLogs
	d.must(t, http.MethodGet, "/api/system/logs?limit=50", nil, &recent)
	n := 0
	for _, entry := range recent.Entries {
		if entry.Message == "dex ignores this .env file" && entry.Attrs != nil && strings.Contains(*entry.Attrs, path) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("got %d warnings for %s", n, path)
	}
}

func TestUserUsesVaultHeaderWithoutLeakingValues(t *testing.T) {
	t.Parallel()
	data, home := t.TempDir(), t.TempDir()
	const secret = "private-header-token"
	const ref = "Bearer ${vault:NOTION_TOKEN}"
	mcp := &mcptest.Server{Dir: t.TempDir()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, "bad header", http.StatusUnauthorized)
			return
		}
		mcp.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	seedMCP(t, data, "default", map[string]any{
		"good":     map[string]any{"type": "http", "url": srv.URL, "headers": map[string]string{"Authorization": ref}},
		"missing":  map[string]any{"type": "http", "url": srv.URL, "headers": map[string]string{"Authorization": "${vault:ABSENT_HEADER_KEY}"}},
		"disabled": map[string]any{"type": "http", "url": srv.URL, "disabled": true, "headers": map[string]string{"Authorization": "${ABSENT_HEADER_KEY}"}},
	})
	d := startDexHome(t, app.Config{DataDir: data}, home)
	saveVault(t, d, "default", "dex", "NOTION_TOKEN", secret)
	d.setModel(t, "tool")
	chat := d.newChat(t, `mcp__good__append {"text":"ok"}`)
	d.watch(t, chat).until(t, "HTTP header", answered("tool: appended 1"))
	got := d.extensions(t)
	if got.server("good").State != "connected" || got.server("missing").State != "failed" || got.server("disabled").State != "disabled" || !strings.Contains(got.server("missing").Error, "ABSENT_HEADER_KEY") {
		t.Fatalf("servers: %+v", got.Servers)
	}
	_, body := d.call(t, http.MethodGet, "/api/agents/default/extensions", nil)
	for _, view := range []string{body, taskTool(t, d, chat, "mcp_server_list", map[string]any{}), taskTool(t, d, chat, "mcp_server_get", map[string]any{"name": "good", "level": "agent"})} {
		if strings.Contains(view, secret) || !strings.Contains(view, ref) {
			t.Fatalf("MCP view must show the reference only: %s", view)
		}
	}
}
