package app_test

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestUserRegistersAnMCPClientOnlyWhenAdvertised(t *testing.T) {
	t.Parallel()
	// Given a server with root protected-resource metadata and RFC 8414 issuer metadata.
	var advertised atomic.Bool
	var registered atomic.Int32
	var origin string
	remote := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		switch r.URL.Path {
		case "/mcp":
			w.WriteHeader(http.StatusUnauthorized)
			return
		case "/.well-known/oauth-protected-resource":
			data = map[string]any{"resource": origin + "/mcp", "authorization_servers": []string{origin}}
		case "/.well-known/oauth-authorization-server":
			data = map[string]any{"issuer": origin, "authorization_endpoint": origin + "/authorize", "token_endpoint": origin + "/token", "code_challenge_methods_supported": []string{"S256"}, "response_types_supported": []string{"code"}}
			if advertised.Load() {
				data["registration_endpoint"] = origin + "/clients"
			}
		case "/clients":
			registered.Add(1)
			var metadata struct {
				RedirectURIs []string `json:"redirect_uris"`
				AuthMethod   string   `json:"token_endpoint_auth_method"`
			}
			if r.Method != http.MethodPost || json.UnmarshalRead(r.Body, &metadata) != nil || len(metadata.RedirectURIs) != 1 || !strings.HasSuffix(metadata.RedirectURIs[0], "/mcp/oauth/callback") || metadata.AuthMethod != "none" {
				t.Error("invalid OAuth client registration")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			data = map[string]any{"client_id": "registered-test-client", "redirect_uris": metadata.RedirectURIs, "token_endpoint_auth_method": "none"}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/clients" {
			w.WriteHeader(http.StatusCreated)
		}
		if err := json.MarshalWrite(w, data); err != nil {
			t.Error(err)
		}
	}))
	origin = "http://" + remote.Listener.Addr().String()
	remote.Start()
	defer remote.Close()
	data := t.TempDir()
	seedMCP(t, data, "default", map[string]any{"notes": map[string]any{"type": "http", "url": origin + "/mcp", "oauth": map[string]any{}}})
	d := startDex(t, data, "")
	start := func(want int, expected string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, d.srv.URL+"/api/bots/default/integrations/mcp/notes/oauth", strings.NewReader(`{"scope":"agent"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", d.srv.URL)
		res, err := d.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out struct {
			URL   string `json:"authorizationUrl"`
			Error string `json:"error"`
		}
		if err := json.UnmarshalRead(res.Body, &out); err != nil || res.StatusCode != want || !strings.Contains(out.URL+out.Error, expected) {
			t.Fatalf("start: %d %+v (%v)", res.StatusCode, out, err)
		}
	}
	// When the provider does not advertise registration, dex asks for a client ID.
	start(400, "pre-registered OAuth client ID")
	if registered.Load() != 0 {
		t.Fatal("dex tried unadvertised registration")
	}
	// When registration is advertised, dex uses the registered client in the browser URL.
	advertised.Store(true)
	start(200, "client_id=registered-test-client")
	if registered.Load() != 1 {
		t.Fatalf("registrations: %d", registered.Load())
	}
}
