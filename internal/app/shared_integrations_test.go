package app_test

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/app"
)

const sharedIntegrationsPath = "/api/integrations"
const sharedMcpPath = sharedIntegrationsPath + "/mcp"

func TestUserListsAndAddsSharedMcpIntegrations(t *testing.T) {
	t.Parallel()
	// Given a token-protected Dex with shared and Bot declarations.
	data := t.TempDir()
	store := filepath.Join(data, ".agents", "skills")
	skillPath := writeSkill(t, store, "guide", "description: Shared guide")
	writeSkill(t, store, "second", "name: guide")
	writeFile(t, filepath.Join(data, ".agents", ".skill-import-warnings", "guide"), "guide: skipped external link\n")
	remote := httpServer(t, t.TempDir())["url"].(string) //nolint:forcetypeassert // The fixture returns a string URL.
	seedMCP(t, data, "", map[string]any{
		"seeded": map[string]any{"type": "http", "url": remote + "?token=query-value", "headers": map[string]string{"Authorization": "plain-header"}, "env": map[string]string{"TOKEN": "plain-env ${vault:TOKEN}"}},
		"off":    map[string]bool{"disabled": true},
	})
	seedMCP(t, data, "default", map[string]any{"overridden": map[string]bool{"disabled": true}})
	const token = "local-dex-owner-test-token"
	d := startDexWith(t, app.Config{DataDir: data, Token: token})
	send := func(method, path, body, origin string, owner bool, want int) string {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, d.srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if owner {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
			req.Header.Set("Sec-Fetch-Site", "cross-site")
		}
		res, err := d.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, res.StatusCode, raw, want)
		}
		return string(raw)
	}

	var other struct {
		ID string `json:"id"`
	}
	raw := send(http.MethodPost, "/api/bots", `{"name":"Other bot","provider":"faux","modelId":"echo","thinkingLevel":"off"}`, "", true, http.StatusCreated)
	if err := json.Unmarshal([]byte(raw), &other); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, filepath.Join(data, "agents", other.ID, ".agents", "skills"), "private", "description: Bot guide")
	body := func(name string) string { return `{"name":"` + name + `","url":"` + remote + `"}` }
	send(http.MethodPost, "/api/bots/"+other.ID+"/integrations/mcp", body("private"), "", true, http.StatusCreated)

	// When a client omits the token or posts from another site, Dex refuses access.
	send(http.MethodGet, sharedIntegrationsPath, "", "", false, http.StatusUnauthorized)
	for _, path := range []string{sharedMcpPath, sharedMcpPath + "/seeded/oauth"} {
		send(http.MethodPost, path, "", "", false, http.StatusUnauthorized)
		send(http.MethodPost, path, "", "https://other.example.test", true, http.StatusForbidden)
	}
	view := func(path string) extensions {
		t.Helper()
		raw := send(http.MethodGet, path, "", "", true, http.StatusOK)
		if path == sharedIntegrationsPath {
			for _, hidden := range []string{"plain-header", "plain-env", "query-value"} {
				if strings.Contains(raw, hidden) {
					t.Fatalf("%s disclosed an MCP value: %s", path, raw)
				}
			}
		}
		var out extensions
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// Then the global view has only shared skills and stored MCP state.
	got := view(sharedIntegrationsPath)
	if len(got.Skills) != 1 || got.Skills[0].Name != "guide" || got.Skills[0].Scope != "dex" || got.Skills[0].Path != skillPath || got.Skills[0].Overrides {
		t.Fatalf("shared skills: %+v", got.Skills)
	}
	if !slices.Contains(got.Errors, "dex/second/SKILL.md: duplicate skill name guide") || !slices.Contains(got.Errors, "dex/guide: skipped external link") {
		t.Fatalf("shared skill warnings: %v", got.Errors)
	}
	seeded, off := got.server("seeded"), got.server("off")
	if len(got.Servers) != 2 || seeded.Scope != "dex" || seeded.State != "idle" || seeded.Tools == nil || len(seeded.Tools) != 0 || off.Scope != "dex" || off.State != "disabled" {
		t.Fatalf("shared MCP servers: %+v", got.Servers)
	}

	// When the owner sends unknown fields, Dex returns a generic error and stores nothing.
	for _, extra := range []string{`,"headers":{}`, `,"oauth":{"accessToken":"not-stored"}`} {
		raw := send(http.MethodPost, sharedMcpPath, `{"name":"bad","url":"`+remote+`"`+extra+`}`, "", true, http.StatusBadRequest)
		if !strings.Contains(raw, `"the request is not valid"`) {
			t.Fatalf("invalid declaration error: %s", raw)
		}
	}
	if got := view(sharedIntegrationsPath).server("bad"); got.Name != "" {
		t.Fatalf("invalid request stored a declaration: %+v", got)
	}

	// When the owner adds a declaration, it is shared and a second create cannot replace it.
	send(http.MethodPost, sharedMcpPath, body("added"), "", true, http.StatusCreated)
	send(http.MethodPost, sharedMcpPath, body("added"), "", true, http.StatusConflict)
	// A Bot can still override a new shared declaration with the same name.
	send(http.MethodPost, sharedMcpPath, body("overridden"), "", true, http.StatusCreated)
	if got := view(sharedIntegrationsPath).server("overridden"); got.Scope != "dex" || got.Overrides || got.State != "idle" {
		t.Fatalf("shared declaration with a Bot override: %+v", got)
	}
	bot := view("/api/bots/default/integrations")
	if got := bot.server("added"); got.Scope != "dex" || got.Overrides {
		t.Fatalf("inherited declaration: %+v", got)
	}
	if got := bot.server("overridden"); got.Scope != "agent" || !got.Overrides || got.State != "disabled" {
		t.Fatalf("Bot override: %+v", got)
	}
}

func TestUserAuthorizesSharedMcpDespiteBotOverride(t *testing.T) {
	t.Parallel()
	// Given a protected shared MCP server that the default Bot disables for itself.
	fixture := startMcpOAuthFixture(t)
	data := t.TempDir()
	config := map[string]any{"type": "http", "url": fixture.origin + "/mcp", "oauth": map[string]string{"clientId": "notes-client", "clientSecret": "vault:NOTES_OAUTH"}}
	seedMCP(t, data, "", map[string]any{"notes": config})
	seedMCP(t, data, "default", map[string]any{"notes": map[string]bool{"disabled": true}, "bot-only": config})
	const token = "local-dex-owner-test-token"
	d := startDexWith(t, app.Config{DataDir: data, Token: token})
	client := d.srv.Client()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	d.must(t, http.MethodPost, "/api/session", map[string]string{"token": token}, nil, http.StatusNoContent)
	d.must(t, http.MethodPut, "/api/bots/default/vault", map[string]string{"scope": "dex", "name": "NOTES_OAUTH", "value": "private-client-secret"}, nil)
	start := func(name, origin string, want int) string {
		t.Helper()
		path := sharedMcpPath + "/" + name + "/oauth"
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("POST %s: %d, want %d", path, res.StatusCode, want)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("POST %s: missing no-store", path)
		}
		if want != http.StatusOK {
			return ""
		}
		if res.Header.Get("Set-Cookie") == "" {
			t.Fatal("missing browser binding cookie")
		}
		var out struct {
			URL string `json:"authorizationUrl"`
		}
		if err := json.UnmarshalRead(res.Body, &out); err != nil {
			t.Fatal(err)
		}
		return out.URL
	}

	// When the owner starts from the Dex origin, only a shared OAuth target is eligible.
	auth, err := url.Parse(start("notes", d.srv.URL, http.StatusOK))
	if err != nil {
		t.Fatal(err)
	}
	start("notes", "", http.StatusBadRequest)
	start("bot-only", d.srv.URL, http.StatusNotFound)
	q := auth.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("redirect_uri") != d.srv.URL+"/mcp/oauth/callback" {
		t.Fatalf("authorization request: %v", q)
	}
	fixture.challenge.Store(q.Get("code_challenge"))
	callback := "/mcp/oauth/callback?" + url.Values{"state": {q.Get("state")}, "code": {"good"}, "iss": {fixture.origin + "/tenant"}}.Encode()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, d.srv.URL+callback, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	// Then the callback returns to global Integrations and the grant belongs to the shared declaration.
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/integrations?section=mcp" {
		t.Fatalf("callback: %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	var got extensions
	d.must(t, http.MethodGet, sharedIntegrationsPath, nil, &got)
	if server := got.server("notes"); server.Scope != "dex" || server.OAuth != "authorized" || server.State != "idle" {
		t.Fatalf("shared OAuth status: %+v", server)
	}
}
