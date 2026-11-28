package app_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/mcptest"
)

type mcpOAuthFixture struct {
	origin    string
	challenge atomic.Value
	refreshes atomic.Int32
	exchanges atomic.Int32
	revoked   atomic.Bool
}

func startMcpOAuthFixture(t *testing.T) *mcpOAuthFixture {
	t.Helper()
	fixture := &mcpOAuthFixture{}
	notes := &mcptest.Server{Dir: t.TempDir()}
	writeFile(t, filepath.Join(notes.Dir, "notes.txt"), "read-only fixture fresh-access-token")
	mux := http.NewServeMux()
	respond := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.MarshalWrite(w, value); err != nil {
			t.Error(err)
		}
	}
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh-access-token" || fixture.revoked.Load() {
			w.Header().Set("WWW-Authenticate", `Basic realm="fallback, realm", Bearer resource_metadata="`+fixture.origin+`/resource", scope="read"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		notes.ServeHTTP(w, r)
	})
	mux.HandleFunc("/resource", func(w http.ResponseWriter, _ *http.Request) {
		respond(w, map[string]any{"resource": fixture.origin + "/mcp", "authorization_servers": []string{fixture.origin + "/tenant"}, "scopes_supported": []string{"write"}})
	})
	mux.HandleFunc("/tenant/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		respond(w, map[string]any{"issuer": fixture.origin + "/tenant", "authorization_endpoint": fixture.origin + "/authorize", "token_endpoint": fixture.origin + "/token", "response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"client_secret_post"}, "authorization_response_iss_parameter_supported": true})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Form.Get("resource") != fixture.origin+"/mcp" || r.Form.Get("client_id") != "notes-client" || r.Form.Get("client_secret") != "private-client-secret" {
			t.Error("missing token request binding")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Form.Get("grant_type") == "refresh_token" {
			if r.Form.Get("refresh_token") != "private-refresh-token" {
				t.Error("wrong refresh token")
			}
			fixture.refreshes.Add(1)
			if fixture.revoked.Load() {
				w.WriteHeader(http.StatusBadRequest)
				respond(w, map[string]string{"error": "invalid_grant", "error_description": "private-refresh-token"})
				return
			}
			respond(w, map[string]any{"access_token": "fresh-access-token", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		fixture.exchanges.Add(1)
		verifier := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if r.Form.Get("code") != "good" || base64.RawURLEncoding.EncodeToString(verifier[:]) != fixture.challenge.Load() {
			w.WriteHeader(http.StatusBadRequest)
			respond(w, map[string]string{"error": "invalid_grant"})
			return
		}
		respond(w, map[string]any{"access_token": "old-access-token", "refresh_token": "private-refresh-token", "token_type": "Bearer", "expires_in": 0})
	})
	remote := httptest.NewUnstartedServer(mux)
	fixture.origin = "http://" + remote.Listener.Addr().String()
	remote.Start()
	t.Cleanup(remote.Close)
	return fixture
}

func TestUserAuthorizesRemoteMcpOAuth(t *testing.T) {
	t.Parallel()
	// Given a protected MCP server and an issuer with OIDC discovery and PKCE.
	fixture := startMcpOAuthFixture(t)
	origin := fixture.origin
	data := t.TempDir()
	config := map[string]any{"type": "http", "url": origin + "/mcp", "oauth": map[string]string{"clientId": "notes-client", "clientSecret": "vault:NOTES_OAUTH"}}
	seedMCP(t, data, "default", map[string]any{"notes": config, "other": config})
	seedMCP(t, data, "", map[string]any{"notes": config, "shared": config})
	const ownerToken = "local-dex-owner-test-token"
	d := startDexWith(t, app.Config{DataDir: data, Token: ownerToken})
	newBrowser := func() *http.Client {
		t.Helper()
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Client{Jar: jar}
	}
	browser := newBrowser()
	// Check the OAuth redirect without serving the web UI.
	browser.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	d.srv.Client().Jar = newBrowser().Jar
	d.must(t, http.MethodPost, "/api/session", map[string]string{"token": ownerToken}, nil, http.StatusNoContent)
	d.must(t, http.MethodPut, "/api/bots/default/vault", map[string]string{"scope": "agent", "name": "NOTES_OAUTH", "value": "private-client-secret"}, nil)
	var bot struct {
		ID string `json:"id"`
	}
	d.must(t, http.MethodPost, "/api/bots", map[string]string{"name": "Other bot", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &bot, http.StatusCreated)
	post := func(client *http.Client, path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", d.srv.URL)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	status := func(path, body string, want int) {
		t.Helper()
		resp := post(browser, path, body)
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("POST %s: %d, want %d", path, resp.StatusCode, want)
		}
	}
	const startPath = "/api/bots/default/integrations/mcp/notes/oauth"
	const startBody = `{"scope":"agent"}`
	status(startPath, startBody, http.StatusUnauthorized)
	status("/api/session", `{"token":"`+ownerToken+`"}`, http.StatusNoContent)
	status(startPath, `{"scope":"dex"}`, http.StatusNotFound)
	start := func() *url.URL {
		t.Helper()
		resp := post(browser, startPath, startBody)
		defer resp.Body.Close()
		var out struct {
			AuthorizationURL string `json:"authorizationUrl"`
		}
		if resp.StatusCode != http.StatusOK || json.UnmarshalRead(resp.Body, &out) != nil {
			t.Fatalf("start OAuth: %d", resp.StatusCode)
		}
		u, err := url.Parse(out.AuthorizationURL)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		if q.Has("access_type") || q.Has("prompt") {
			t.Fatal("Google offline access parameters reached another issuer")
		}
		if q.Get("scope") != "read" || q.Get("resource") != origin+"/mcp" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" {
			t.Fatalf("wrong authorization request: %v", q)
		}
		fixture.challenge.Store(q.Get("code_challenge"))
		return u
	}
	finish := func(client *http.Client, auth *url.URL, state, code string, want int) {
		t.Helper()
		cb := auth.Query().Get("redirect_uri") + "?" + url.Values{"state": {state}, "code": {code}, "iss": {origin + "/tenant"}}.Encode()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, cb, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("callback: %d, want %d", resp.StatusCode, want)
		}
		if want == http.StatusSeeOther {
			location, err := resp.Location()
			if err != nil || location.Path != "/b/default/integrations" {
				t.Fatalf("callback location %v (%v), want the Bot Integrations page", location, err)
			}
		}
	}
	// When state or the browser binding is wrong, the flow cannot be used.
	auth := start()
	finish(browser, auth, "wrong", "good", 400)
	finish(d.srv.Client(), auth, auth.Query().Get("state"), "good", 400)
	// When the issuer has a different PKCE challenge, exchange fails and state is spent.
	fixture.challenge.Store("wrong challenge")
	finish(browser, auth, auth.Query().Get("state"), "good", 400)
	finish(browser, auth, auth.Query().Get("state"), "good", 400)
	auth = start()
	finish(browser, auth, auth.Query().Get("state"), "good", http.StatusSeeOther)
	finish(browser, auth, auth.Query().Get("state"), "good", 400)
	// Then only this connection gets a grant and an authenticated read succeeds after refresh.
	d.setModel(t, "tool")
	if status, body := d.reload(t); status != 200 {
		t.Fatalf("reload: %d %s", status, body)
	}
	if view := d.extensions(t); view.server("notes").State != "connected" || view.server("other").State == "connected" || view.server("shared").State == "connected" {
		t.Fatalf("MCP isolation: %+v", view.Servers)
	}
	if fixture.refreshes.Load() != 1 {
		t.Fatalf("refreshes: %d", fixture.refreshes.Load())
	}
	var other extensions
	d.must(t, http.MethodGet, "/api/bots/"+bot.ID+"/integrations", nil, &other)
	if s := other.server("notes"); s.Scope != "dex" || s.OAuth != "authorize" {
		t.Fatalf("grant reached shared scope: %+v", s)
	}
	files, err := filepath.Glob(filepath.Join(data, "vault", "mcp-*.age"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one encrypted grant: %v, %v", files, err)
	}
	ciphertext, err := os.ReadFile(files[0]) //nolint:gosec // Test vault path.
	if err != nil || !bytes.HasPrefix(ciphertext, []byte("age-encryption.org/v1")) || bytes.Contains(ciphertext, []byte("private-refresh-token")) {
		t.Fatal("the grant is not encrypted")
	}
	sessionID := d.newSessionWithMessage(t, `mcp__notes__read {}`)
	w := d.watch(t, sessionID)
	w.until(t, "read with no token disclosure", answered("tool: read-only fixture [hidden]"))
	// When the grant is revoked, dex stops calls and asks the owner to reauthorize.
	fixture.revoked.Store(true)
	d.send(t, sessionID, `mcp__notes__read {}`)
	eventually(t, "reauthorize", func() (bool, any) {
		_, body := d.call(t, http.MethodGet, "/api/bots/default/integrations", nil)
		return strings.Contains(body, `"oauth":"reauthorize"`) && !strings.Contains(body, "private-refresh-token"), body
	})
	w.until(t, "reauthorization action", answeredWith("reauthorize this MCP connection in Integrations"))

	// When the resource changes while the browser is away, the old callback cannot exchange its code.
	auth = start()
	before := fixture.exchanges.Load()
	d.send(t, sessionID, `mcp_server_set {"name":"notes","level":"agent","config":{"type":"http","url":"`+origin+`/different","oauth":{"clientId":"notes-client","clientSecret":"vault:NOTES_OAUTH"}}}`)
	w.until(t, "changed MCP resource", answeredWith("Saved. The change applies"))
	finish(browser, auth, auth.Query().Get("state"), "good", http.StatusBadRequest)
	if fixture.exchanges.Load() != before {
		t.Fatal("the code reached the token endpoint after the resource changed")
	}
}
