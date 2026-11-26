package app_test

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/mcptest"
)

type createdServer struct {
	Name      string   `json:"name"`
	Scope     string   `json:"scope"`
	Overrides bool     `json:"overrides"`
	Transport string   `json:"transport"`
	State     string   `json:"state"`
	Tools     []string `json:"tools"`
	OAuth     string   `json:"oauth"`
}

func TestUserAddsCustomRemoteMcpServerToOneBotFromWeb(t *testing.T) {
	t.Parallel()
	// Given a token-protected Dex with a Dex-level declaration, a second Bot with its own
	// declaration, a Bot vault secret, and a local MCP server over Streamable HTTP.
	notes := &mcptest.Server{Dir: t.TempDir()}
	writeFile(t, filepath.Join(notes.Dir, "notes.txt"), "local fixture")
	var leaked atomic.Bool
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
		notes.ServeHTTP(w, r)
	}))
	defer remote.Close()
	data := t.TempDir()
	seedMCP(t, data, "", map[string]any{"shared": map[string]any{"type": "http", "url": remote.URL + "/mcp"}})
	const ownerToken = "local-dex-owner-test-token"
	const secretValue = "private-client-secret-value"
	d := startDexWith(t, app.Config{DataDir: data, Token: ownerToken})
	owner := map[string]string{"Authorization": "Bearer " + ownerToken}
	send := func(method, path, body string, header map[string]string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, d.srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := d.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), secretValue) {
			t.Fatalf("%s %s disclosed the vault secret: %s", method, path, raw)
		}
		return resp.StatusCode, string(raw)
	}
	expect := func(method, path, body string, want int) string {
		t.Helper()
		status, raw := send(method, path, body, owner)
		if status != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, status, raw, want)
		}
		return raw
	}
	expect(http.MethodPut, "/api/bots/default/vault", `{"scope":"agent","name":"NOTES_CLIENT","value":"`+secretValue+`"}`, http.StatusOK)
	var other struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(expect(http.MethodPost, "/api/bots", `{"name":"Other bot","provider":"faux","modelId":"echo","thinkingLevel":"off"}`, http.StatusCreated)), &other); err != nil {
		t.Fatal(err)
	}
	servers := func(botID string) map[string]createdServer {
		t.Helper()
		var view struct {
			Servers []createdServer `json:"servers"`
		}
		if err := json.Unmarshal([]byte(expect(http.MethodGet, "/api/bots/"+botID+"/integrations", "", http.StatusOK)), &view); err != nil {
			t.Fatal(err)
		}
		out := map[string]createdServer{}
		for _, s := range view.Servers {
			out[s.Name] = s
		}
		return out
	}
	create := "/api/bots/default/integrations/mcp"
	oauthBody := `{"name":"notes","url":"` + remote.URL + `/mcp","oauth":{"clientId":"notes-client","clientSecret":"vault:NOTES_CLIENT"}}`

	// When a client without the owner token adds a declaration, then Dex refuses it.
	if status, _ := send(http.MethodPost, create, oauthBody, nil); status != http.StatusUnauthorized {
		t.Fatalf("POST without a token: %d", status)
	}
	// When another site posts with the owner's credentials, then Dex refuses it.
	cross := map[string]string{"Authorization": owner["Authorization"], "Origin": "https://attacker.example", "Sec-Fetch-Site": "cross-site"}
	if status, _ := send(http.MethodPost, create, oauthBody, cross); status != http.StatusForbidden {
		t.Fatalf("cross-site POST: %d", status)
	}
	// Authentication runs before the body is read, so a malformed request without the token is 401.
	headerBody := `{"name":"bad","url":"https://mcp.example.test/mcp","headers":{"Authorization":"Bearer ` + secretValue + `"}}`
	if status, _ := send(http.MethodPost, create, headerBody, nil); status != http.StatusUnauthorized {
		t.Fatalf("POST with an unknown field and without a token: %d", status)
	}
	if _, ok := servers("default")["notes"]; ok {
		t.Fatal("a refused request stored a declaration")
	}

	// When the request has credentials in the URL, a query, a fragment, plain HTTP to a public
	// host, a plaintext client secret, an invalid name, or a field that the API does not define,
	// then Dex refuses it with a generic error, stores nothing, and does not echo it.
	for _, body := range []string{
		`{"name":"bad","url":"https://user:` + secretValue + `@mcp.example.test/mcp"}`,
		`{"name":"bad","url":"https://mcp.example.test/mcp?token=` + secretValue + `"}`,
		`{"name":"bad","url":"https://mcp.example.test/mcp#` + secretValue + `"}`,
		`{"name":"bad","url":"http://mcp.example.test/mcp"}`,
		`{"name":"bad","url":"https://mcp.example.test/mcp","oauth":{"clientId":"c","clientSecret":"` + secretValue + `"}}`,
		`{"name":"bad","url":"https://mcp.example.test/mcp","oauth":{"clientSecret":"vault:NOTES_CLIENT"}}`,
		`{"name":"bad name","url":"https://mcp.example.test/mcp"}`,
		`{"name":"bad","url":"https://mcp.example.test/mcp","oauth":{"clientId":"c","clientSecret":7}}`,
	} {
		if status, raw := send(http.MethodPost, create, body, owner); status != http.StatusBadRequest {
			t.Fatalf("POST %s: %d %s, want 400", body, status, raw)
		}
	}
	// Unknown fields are refused, not dropped: Dex would not send the authentication they name.
	for _, body := range []string{
		headerBody,
		`{"name":"bad","url":"https://mcp.example.test/mcp","token":"` + secretValue + `"}`,
		`{"name":"bad","url":"https://mcp.example.test/mcp","oauth":{"clientId":"c","accessToken":"` + secretValue + `"}}`,
		`{"name":"bad","url":"https://mcp.example.test/mcp","oauth":{"headers":{"Authorization":"Bearer ` + secretValue + `"}}}`,
	} {
		status, raw := send(http.MethodPost, create, body, owner)
		if status != http.StatusBadRequest || !strings.Contains(raw, `"the request is not valid"`) {
			t.Fatalf("POST %s: %d %s, want 400 with the generic error", body, status, raw)
		}
	}
	if _, ok := servers("default")["bad"]; ok {
		t.Fatal("an invalid request stored a declaration")
	}

	// When the owner adds a public server to the other Bot first, and then adds an OAuth server
	// with the same name to the default Bot.
	expect(http.MethodPost, "/api/bots/"+other.ID+"/integrations/mcp", `{"name":"notes","url":"`+remote.URL+`/mcp"}`, http.StatusCreated)
	expect(http.MethodPost, create, oauthBody, http.StatusCreated)

	// Then the default Bot lists a Bot-level OAuth declaration that still needs authorization.
	// Saving does not claim a live connection or tools.
	var got createdServer
	eventually(t, "default notes declaration", func() (bool, any) {
		got = servers("default")["notes"]
		return got.Name != "", got
	})
	if got.Scope != "agent" || got.Overrides || got.Transport != "http" || got.OAuth != "authorize" || got.State == "connected" || len(got.Tools) != 0 {
		t.Fatalf("default notes = %+v", got)
	}
	// The other Bot keeps its own public declaration and sends no Authorization header.
	var mine createdServer
	eventually(t, "other notes connection", func() (bool, any) {
		mine = servers(other.ID)["notes"]
		if mine.State == "connected" {
			return true, mine
		}
		// A busy bot answers 409; the next poll tries again.
		if status, raw := send(http.MethodPost, "/api/bots/"+other.ID+"/integrations/reload", "", owner); status != http.StatusOK && status != http.StatusConflict {
			t.Fatalf("reload other bot: %d %s", status, raw)
		}
		return false, mine
	})
	if mine.Scope != "agent" || mine.OAuth != "" || mine.State != "connected" || len(mine.Tools) == 0 {
		t.Fatalf("other notes = %+v", mine)
	}
	if leaked.Load() {
		t.Fatal("Dex sent an Authorization header to a public server")
	}
	if _, ok := servers(other.ID)["shared"]; !ok {
		t.Fatal("the other Bot lost the inherited Dex declaration")
	}

	// When the owner chooses OAuth without preset client credentials, then the declaration
	// still needs authorization. An empty OAuth object requests discovery and registration.
	expect(http.MethodPost, create, `{"name":"discover","url":"`+remote.URL+`/mcp","oauth":{}}`, http.StatusCreated)
	if got := servers("default")["discover"]; got.OAuth != "authorize" || got.State == "connected" {
		t.Fatalf("discovery-only OAuth = %+v, want authorization before connection", got)
	}

	// When the owner adds the same name again, or a name that a Dex-level declaration uses, then
	// Dex refuses it and keeps the stored declarations.
	expect(http.MethodPost, create, `{"name":"notes","url":"`+remote.URL+`/mcp"}`, http.StatusConflict)
	expect(http.MethodPost, create, `{"name":"shared","url":"`+remote.URL+`/mcp"}`, http.StatusConflict)
	after := servers("default")
	if after["notes"].OAuth != "authorize" || after["shared"].Scope != "dex" || after["shared"].Overrides {
		t.Fatalf("collisions changed declarations: %+v", after)
	}

	// When several requests add the same new name at once, then exactly one succeeds.
	var wg sync.WaitGroup
	statuses := make(chan int, 8)
	for range 8 {
		wg.Go(func() {
			status, _ := send(http.MethodPost, create, `{"name":"race","url":"`+remote.URL+`/mcp"}`, owner)
			statuses <- status
		})
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[http.StatusCreated] != 1 || counts[http.StatusConflict] != 7 {
		t.Fatalf("concurrent creates: %v", counts)
	}

	// When the owner adds a declaration for a Bot that does not exist, then Dex refuses it.
	expect(http.MethodPost, "/api/bots/missing/integrations/mcp", `{"name":"x","url":"`+remote.URL+`/mcp"}`, http.StatusNotFound)
}
