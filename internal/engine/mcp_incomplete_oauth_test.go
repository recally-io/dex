package engine

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/mcp/oauth"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/mcptest"
)

func TestUserLegacyOAuthIdentityWithoutSettingsIsRefusedUntilReplaced(t *testing.T) {
	t.Parallel()
	// Given a control.sqlite row from an old writer: the declaration has an
	// OAuth identity, but its OAuth settings are missing.
	a, _, data := extensionFixture(t)
	a.mcp = testMCPStore(t, data)
	var legacyRequests atomic.Int64
	fixture := &mcptest.Server{Dir: t.TempDir()}
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyRequests.Add(1)
		fixture.ServeHTTP(w, r)
	}))
	defer legacy.Close()
	plain := httptest.NewServer(&mcptest.Server{Dir: t.TempDir()})
	defer plain.Close()
	const legacyID = "legacy-oauth-id"
	row := `{"type":"http","url":"` + legacy.URL + `","oauthId":"` + legacyID + `"}`
	q := sqlc.New(a.mcp.db)
	if err := q.SetMCPServer(t.Context(), sqlc.SetMCPServerParams{BotID: "", Name: "legacy", Config: row}); err != nil {
		t.Fatal(err)
	}
	// And an ordinary HTTP declaration next to it.
	testSetMCP(t, a.mcp, "", "plain", serverConfig{Type: "http", URL: plain.URL})
	// And a synthetic encrypted grant that is bound to the legacy identity.
	stale := serverEntry{info: McpServerInfo{Name: "legacy", Scope: "dex"}, config: serverConfig{Type: "http", URL: legacy.URL, OAuthID: legacyID}}
	if err := stale.saveGrant(a.vault, &mcpGrant{Binding: stale.grantBinding(), Tokens: &oauth.OAuthTokens{AccessToken: "synthetic-access", TokenType: "Bearer"}}); err != nil {
		t.Fatal(err)
	}
	tools := (&Bots{config: Config{MCPServers: a.mcp}}).mcpServerTools("a", nil)
	call := func(t *testing.T, name string, args map[string]any) string {
		t.Helper()
		i := slices.IndexFunc(tools, func(tool *durable.ToolRegistration) bool { return tool.Name == name })
		if i < 0 {
			t.Fatalf("tool %s is not registered", name)
		}
		args["name"], args["level"] = "legacy", "dex"
		result, err := tools[i].Execute(t.Context(), args, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return ai.ContentText(result.Output)
	}
	storedRow := func(t *testing.T) string {
		t.Helper()
		raw, err := q.GetMCPServer(t.Context(), sqlc.GetMCPServerParams{BotID: "", Name: "legacy"})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	named := func(servers []McpServerInfo, name string) McpServerInfo {
		i := slices.IndexFunc(servers, func(s McpServerInfo) bool { return s.Name == name })
		if i < 0 {
			t.Fatalf("server %s absent: %#v", name, servers)
		}
		return servers[i]
	}

	// When the bot reads the declaration,
	// then mcp_server_get explains the problem and the repair without the identity or the grant.
	text := call(t, "mcp_server_get", map[string]any{})
	if !strings.Contains(text, `"error":`) || !strings.Contains(text, "no OAuth settings") || !strings.Contains(text, "mcp_server_set") {
		t.Fatalf("mcp_server_get has no actionable diagnostic: %s", text)
	}
	if strings.Contains(text, legacyID) || strings.Contains(text, "synthetic-access") {
		t.Fatalf("mcp_server_get leaked OAuth state: %s", text)
	}

	// When the bot loads its connections,
	// then dex refuses the incomplete declaration before it contacts the server,
	// and the ordinary HTTP declaration still connects.
	loaded, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close(t.Context())
	if s := named(loaded.Servers, "legacy"); s.State != "failed" || !strings.Contains(s.Error, "no OAuth settings") || !strings.Contains(s.Error, "mcp_server_set") || len(s.Tools) != 0 {
		t.Fatalf("incomplete declaration was not refused: %#v", s)
	}
	if s := named(loaded.Servers, "plain"); s.State != "connected" || len(s.Tools) == 0 {
		t.Fatalf("ordinary HTTP declaration changed: %#v", s)
	}
	if n := legacyRequests.Load(); n != 0 {
		t.Fatalf("dex contacted the incomplete declaration %d times", n)
	}
	// And the stored declaration is unchanged.
	if got := storedRow(t); got != row {
		t.Fatalf("stored declaration changed without a replacement: %s", got)
	}

	// When the owner explicitly replaces the declaration with a pre-registered OAuth client
	// through the existing set operation, and the bot reloads,
	if text := call(t, "mcp_server_set", map[string]any{"config": map[string]any{
		"type": "http", "url": legacy.URL, "oauth": map[string]any{"clientId": "synthetic-client"},
	}}); !strings.Contains(text, "finishes its current work and reloads") {
		t.Fatalf("replacement did not report idle reload: %s", text)
	}

	// then mcp_server_get shows the OAuth settings without a diagnostic,
	text = call(t, "mcp_server_get", map[string]any{})
	if strings.Contains(text, `"error"`) || !strings.Contains(text, `"clientId":"synthetic-client"`) || strings.Contains(text, legacyID) {
		t.Fatalf("replaced declaration: %s", text)
	}
	// and the declaration has a fresh OAuth identity,
	replaced, err := a.mcp.get(t.Context(), "", "legacy")
	if err != nil || replaced.OAuthID == "" || replaced.OAuthID == legacyID {
		t.Fatalf("replacement identity: %q %v", replaced.OAuthID, err)
	}
	// and the connection requires authorization: the legacy grant is not reused.
	reloaded, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close(t.Context())
	if s := named(reloaded.Servers, "legacy"); s.State != "failed" || s.Error != errMCPAuthorize.Error() || s.OAuth != "authorize" {
		t.Fatalf("replaced declaration is not authorization-required: %#v", s)
	}
	if s := named(reloaded.Servers, "plain"); s.State != "connected" {
		t.Fatalf("ordinary HTTP declaration changed: %#v", s)
	}
	if n := legacyRequests.Load(); n != 0 {
		t.Fatalf("dex contacted the server without a grant %d times", n)
	}
}
