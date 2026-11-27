package app_test

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/mcptest"
)

func TestUserSearchesIntegrationCatalogs(t *testing.T) {
	t.Parallel()
	// Given an owner-only API and local catalog upstreams with extra fields and mixed transports.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("Dex forwarded owner credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query()
		if q.Get("q") == "fail" || q.Get("search") == "fail" {
			http.Error(w, "private upstream error", http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/api/skills/search":
			if q.Get("q") != "test & pdf" || q.Get("limit") != "20" || q.Get("offset") != "20" {
				t.Errorf("skill upstream query: %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"skills":[{"id":"pdf","name":"PDF","sourceUrl":"https://github.com/acme/skills/tree/main/pdf","description":"Read PDFs","metadata":{"directoryPath":"pdf","rawFileUrl":"https://hidden.example"},"dependencies":["omitted"]}],"total":41,"limit":20,"offset":20}`)
		case "/v0/servers":
			if q.Get("search") != "acme" || q.Get("cursor") != "opaque+cursor" || q.Get("version") != "latest" || q.Get("limit") != "20" {
				t.Errorf("MCP upstream query: %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"servers":[
				{"server":{"name":"acme/mixed","title":"Mixed","description":"Tools","version":"1","remotes":[{"type":"streamable-http","url":"https://{tenant}.example/mcp"},{"type":"streamable-http","url":"https://acme.example/mcp","headers":[{"name":"Authorization","isRequired":true,"isSecret":true,"value":"omit-me"},{"name":"Accept-Language","value":"en"}]},{"type":"sse","url":"https://acme.example/old"}]}},
				{"server":{"name":"acme/local","packages":[{"identifier":"omit-me"}]}},
				{"server":{"name":"acme/old","remotes":[{"type":"sse","url":"https://acme.example/old"}]}},
				{"server":{"name":"acme/tenant","remotes":[{"type":"streamable-http","url":"https://{tenant}.example/mcp"}]}},
				{"server":{"name":"acme/query","remotes":[{"type":"streamable-http","url":"https://acme.example/mcp?token=value"}]}}
			],"metadata":{"nextCursor":"next"}}`)
		default:
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	const token = "catalog-owner"
	d := startDexWith(t, app.Config{
		DataDir: t.TempDir(), Token: token, SkillCatalogURL: upstream.URL, McpRegistryURL: upstream.URL,
	})
	get := func(path string, auth bool, want int) string {
		t.Helper()
		r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, d.srv.URL+"/api/integrations/catalog/"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth {
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Cookie", "private=cookie")
		}
		resp, err := d.srv.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != want {
			t.Fatalf("%s: %d %s (%v), want %d", path, resp.StatusCode, raw, err, want)
		}
		return string(raw)
	}
	// When the owner searches, then Dex pages the results and returns only useful fields.
	skills := get("skills?q="+url.QueryEscape(" test & pdf ")+"&offset=20", true, http.StatusOK)
	containsCatalogFields(t, skills, `"name":"PDF"`, `"directoryPath":"pdf"`, `"nextOffset":40`)
	mcp := get("mcp?q=acme&cursor=opaque%2Bcursor", true, http.StatusOK)
	var page struct {
		Servers []struct {
			Name    string `json:"name"`
			Addable bool   `json:"addable"`
			Reason  string `json:"reason"`
			Remotes []struct {
				Addable bool   `json:"addable"`
				URL     string `json:"url"`
			} `json:"remotes"`
		} `json:"servers"`
	}
	if err := json.Unmarshal([]byte(mcp), &page); err != nil || len(page.Servers) != 5 {
		t.Fatalf("MCP Registry page: %s (%v)", mcp, err)
	}
	mixed := page.Servers[0]
	if !mixed.Addable || len(mixed.Remotes) != 2 || mixed.Remotes[0].Addable || !mixed.Remotes[1].Addable || mixed.Remotes[1].URL != "https://acme.example/mcp" {
		t.Fatalf("mixed remotes: %+v", mixed)
	}
	for i, reason := range []string{"local package", "No Streamable HTTP remote", "URL variables", "URL does not meet Dex requirements"} {
		if server := page.Servers[i+1]; server.Addable || !strings.Contains(server.Reason, reason) {
			t.Fatalf("expected %q for %+v", reason, server)
		}
	}
	containsCatalogFields(t, mcp, `"isSecret":true`, `"value":"en"`, `"nextCursor":"next"`)
	for _, extra := range []string{"hidden.example", "dependencies", "omit-me", "acme.example/old", "token=value"} {
		if strings.Contains(skills, extra) || strings.Contains(mcp, extra) {
			t.Fatalf("catalog included %q: %s %s", extra, skills, mcp)
		}
	}
	// When a caller is not the owner, passes an invalid page, or the upstream fails, then Dex returns an error.
	get("skills", false, http.StatusUnauthorized)
	get("mcp", false, http.StatusUnauthorized)
	get("skills?offset=-1", true, http.StatusBadRequest)
	get("mcp?cursor="+strings.Repeat("x", 1025), true, http.StatusBadRequest)
	for _, path := range []string{"skills?q=fail", "mcp?q=fail"} {
		if got := get(path, true, http.StatusBadGateway); strings.Contains(got, "private upstream error") {
			t.Fatalf("%s disclosed the upstream error: %s", path, got)
		}
	}
}

func containsCatalogFields(t *testing.T, got string, want ...string) {
	t.Helper()
	var missing []string
	for _, fragment := range want {
		if !strings.Contains(got, fragment) {
			missing = append(missing, fragment)
		}
	}
	if len(missing) != 0 {
		t.Fatalf("missing %v in %s", missing, got)
	}
}

func TestUserAddsRemoteMcpWithVaultHeaders(t *testing.T) {
	t.Parallel()
	// Given a Streamable HTTP MCP server that needs a resolved token and a plain header.
	const secret = "local-mcp-fixture-secret"
	notes := &mcptest.Server{Dir: t.TempDir()}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("X-Client") != "dex" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		notes.ServeHTTP(w, r)
	}))
	t.Cleanup(upstream.Close)
	d := startDexWith(t, app.Config{DataDir: t.TempDir()})
	const vault = "/api/bots/default/vault"
	const botIntegrations = "/api/bots/default/integrations"
	const shared = "/api/integrations/mcp"
	const bot = botIntegrations + "/mcp"
	body := func(name string, headers any) map[string]any {
		return map[string]any{"name": name, "url": upstream.URL + "/mcp", "headers": headers}
	}
	headers := map[string]string{"Authorization": "Bearer ${vault:MCP_TOKEN}", "X-Client": "dex"}
	for _, path := range []string{shared, bot} {
		for _, invalid := range []map[string]any{
			body("invalid", map[string]any{"Authorization": 123}),
			body("invalid", map[string]string{"Authorization": "Bearer " + secret}),
			body("invalid", map[string]string{"X-Bad Header": "dex"}),
			body("invalid", map[string]string{"X-Client": "dex\r\nBad: header"}),
			body("invalid", map[string]string{"X-CLIENT": "dex", "x-client": "other"}),
			{"name": "invalid", "url": upstream.URL, "headers": headers, "oauth": map[string]any{}},
			{"name": "invalid", "url": upstream.URL, "headers": map[string]string{}, "oauth": map[string]any{}},
			{"name": "invalid", "url": upstream.URL, "headers": headers, "env": map[string]any{}},
		} {
			status, raw := d.call(t, http.MethodPost, path, invalid)
			if status != http.StatusBadRequest || strings.Contains(raw, secret) {
				t.Fatalf("invalid headers on %s: %d %s", path, status, raw)
			}
		}
	}
	// When the owner saves declarations at both levels, then they use vault references and do not replace existing entries.
	d.must(t, http.MethodPut, vault, map[string]string{"scope": "dex", "name": "MCP_TOKEN", "value": secret}, nil, http.StatusOK)
	d.must(t, http.MethodPut, vault, map[string]string{"scope": "agent", "name": "BOT_TOKEN", "value": secret}, nil, http.StatusOK)
	d.must(t, http.MethodPost, shared, body("shared-auth", headers), nil, http.StatusCreated)
	d.must(t, http.MethodPost, shared, body("shared-auth", headers), nil, http.StatusConflict)
	d.must(t, http.MethodPost, bot, body("shared-auth", headers), nil, http.StatusConflict)
	botHeaders := map[string]string{"Authorization": "Bearer ${vault:BOT_TOKEN}", "X-Client": "dex"}
	d.must(t, http.MethodPost, bot, body("bot-auth", botHeaders), nil, http.StatusCreated)
	d.must(t, http.MethodPost, bot, body("bot-auth", botHeaders), nil, http.StatusConflict)

	// After reload, both connections list real tools and the API returns only references.
	type server struct {
		Name            string   `json:"name"`
		State           string   `json:"state"`
		Tools           []string `json:"tools"`
		VaultReferences struct {
			Headers map[string]string `json:"headers"`
		} `json:"vaultReferences"`
	}
	type view struct {
		Servers []server `json:"servers"`
	}
	var got view
	eventually(t, "header-authenticated MCP connections", func() (bool, any) {
		d.must(t, http.MethodGet, botIntegrations, nil, &got, http.StatusOK)
		if len(got.Servers) == 2 && !slices.ContainsFunc(got.Servers, func(s server) bool { return s.State != "connected" || len(s.Tools) == 0 }) {
			return true, got
		}
		if status, raw := d.call(t, http.MethodPost, botIntegrations+"/reload", nil); status != http.StatusOK && status != http.StatusConflict {
			t.Fatalf("reload: %d %s", status, raw)
		}
		return false, got
	})
	for _, s := range got.Servers {
		want := botHeaders
		if s.Name == "shared-auth" {
			want = headers
		}
		if len(s.VaultReferences.Headers) != 1 || s.VaultReferences.Headers["Authorization"] != want["Authorization"] {
			t.Errorf("wrong references for %s: %+v", s.Name, s.VaultReferences)
		}
	}
}

func TestUserKeepsMcpHeadersOnTheConfiguredOrigin(t *testing.T) {
	t.Parallel()
	// Given an MCP endpoint that redirects to a different origin.
	var followed atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed.Store(true)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(other.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(origin.Close)
	d := startDex(t, t.TempDir(), "")
	const integrations = "/api/bots/default/integrations"
	d.must(t, http.MethodPut, "/api/bots/default/vault", map[string]string{
		"scope": "agent", "name": "REMOTE_TOKEN", "value": "local-fixture-token",
	}, nil, http.StatusOK)
	d.must(t, http.MethodPost, integrations+"/mcp", map[string]any{
		"name": "redirected", "url": origin.URL, "headers": map[string]string{"X-Client-Key": "${vault:REMOTE_TOKEN}"},
	}, nil, http.StatusCreated)

	// When the Bot connects, then the connection fails without sending a request to the other origin.
	var got struct {
		Servers []createdServer `json:"servers"`
	}
	d.must(t, http.MethodPost, integrations+"/reload", nil, &got, http.StatusOK)
	if followed.Load() || len(got.Servers) != 1 || got.Servers[0].State != "failed" {
		t.Fatalf("redirect followed=%t, connections=%+v", followed.Load(), got.Servers)
	}
}
