package app_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/mcptest"
)

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
	session := d.newSessionWithMessage(t, `mcp__good__append {"text":"ok"}`)
	d.watch(t, session).until(t, "HTTP header", answered("tool: appended 1"))
	got := d.extensions(t)
	if got.server("good").State != "connected" || got.server("missing").State != "failed" || got.server("disabled").State != "disabled" || !strings.Contains(got.server("missing").Error, "ABSENT_HEADER_KEY") {
		t.Fatalf("servers: %+v", got.Servers)
	}
	_, body := d.call(t, http.MethodGet, "/api/bots/default/integrations", nil)
	for _, view := range []string{body, taskTool(t, d, session, "mcp_server_list", map[string]any{}), taskTool(t, d, session, "mcp_server_get", map[string]any{"name": "good", "level": "agent"})} {
		if strings.Contains(view, secret) || !strings.Contains(view, ref) {
			t.Fatalf("MCP view must show the reference only: %s", view)
		}
	}
}
