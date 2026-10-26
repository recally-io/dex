package app_test

import (
	"database/sql"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/service"
	"github.com/recally-io/dex/internal/settings"
)

type reply struct {
	StatusCode int
	cookies    []*http.Cookie
}

func (r reply) Cookies() []*http.Cookie { return r.cookies }

// request sends a request with optional headers and returns its status and cookies.
func (d *dex) request(t *testing.T, method, path, body string, header map[string]string) reply {
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
	_ = resp.Body.Close()
	return reply{resp.StatusCode, resp.Cookies()}
}

func TestUserOnNetworkHostNeedsTheStoredToken(t *testing.T) {
	t.Parallel()
	// Given dex on a host that is not loopback, which requires the stored token. The CLI reads
	// the token through a file: URI, which must escape the space.
	dataDir := filepath.Join(t.TempDir(), "data dir")
	d := startDexWith(t, app.Config{DataDir: dataDir, RequireToken: true})

	// When a client has no token, then the API refuses it, health included.
	for _, path := range []string{"/api/agents/default/chats", "/api/health", "/api/agents/default/uploads/x", "/api/agents/default/chats/1/events"} {
		if resp := d.request(t, http.MethodGet, path, "", nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET %s without a token: %d", path, resp.StatusCode)
		}
	}
	// The web UI files stay public, so the browser can show the sign-in form.
	if resp := d.request(t, http.MethodGet, "/", "", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / without a token: %d", resp.StatusCode)
	}

	// When the CLI reads the token while dex runs, then the token opens the API.
	token, err := settings.StoredToken(t.Context(), dataDir)
	if err != nil || settings.CheckToken(token) != nil {
		t.Fatalf("stored token %q: %v", token, err)
	}
	bearer := map[string]string{"Authorization": "Bearer " + token}
	if resp := d.request(t, http.MethodGet, "/api/health", "", bearer); resp.StatusCode != http.StatusOK {
		t.Fatalf("health with the token: %d", resp.StatusCode)
	}
	if resp := d.request(t, http.MethodGet, "/api/agents/default/chats", "", map[string]string{"Authorization": "Bearer " + token + "x"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a wrong token: %d", resp.StatusCode)
	}

	// When the browser signs in, then a wrong token gets no cookie and the right one gets a
	// host-only, HttpOnly, SameSite=Strict cookie that opens the API.
	if resp := d.request(t, http.MethodPost, "/api/session", `{"token":"wrong"}`, nil); resp.StatusCode != http.StatusUnauthorized || len(resp.Cookies()) != 0 {
		t.Fatalf("sign-in with a wrong token: %d %v", resp.StatusCode, resp.Cookies())
	}
	resp := d.request(t, http.MethodPost, "/api/session", `{"token":"`+token+`"}`, nil)
	cookies := resp.Cookies()
	if resp.StatusCode != http.StatusNoContent || len(cookies) != 1 {
		t.Fatalf("sign-in: %d %v", resp.StatusCode, cookies)
	}
	c := cookies[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Domain != "" || c.Path != "/" {
		t.Fatalf("session cookie: %+v", c)
	}
	session := map[string]string{"Cookie": c.String()}
	for _, path := range []string{"/api/agents/default/chats", "/api/health"} {
		if resp := d.request(t, http.MethodGet, path, "", session); resp.StatusCode/100 != 2 {
			t.Fatalf("GET %s with the cookie: %d", path, resp.StatusCode)
		}
	}

	// When another site posts the token from the user's browser, then dex refuses it.
	cross := map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://example.com"}
	if resp := d.request(t, http.MethodPost, "/api/session", `{"token":"`+token+`"}`, cross); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site sign-in: %d", resp.StatusCode)
	}

	// When dex restarts, then the stored token stays the same.
	d.stop()
	d = startDexWith(t, app.Config{DataDir: dataDir, RequireToken: true})
	if resp := d.request(t, http.MethodGet, "/api/agents/default/chats", "", bearer); resp.StatusCode != http.StatusOK {
		t.Fatalf("the token after a restart: %d", resp.StatusCode)
	}
}

func TestUserTokenFromTheEnvironmentReplacesTheStoredToken(t *testing.T) {
	t.Parallel()
	// Given dex with DEX_TOKEN.
	dataDir := t.TempDir()
	own := "user-token-0123456789abcdefghijklmnopqrstuvwxyz"
	d := startDexWith(t, app.Config{DataDir: dataDir, Token: own, RequireToken: true})
	stored, err := settings.StoredToken(t.Context(), dataDir)
	if err != nil || stored == "" {
		t.Fatalf("stored token %q: %v", stored, err)
	}

	// Then only the user's token opens the API.
	if resp := d.request(t, http.MethodGet, "/api/agents/default/chats", "", map[string]string{"Authorization": "Bearer " + own}); resp.StatusCode != http.StatusOK {
		t.Fatalf("the user's token: %d", resp.StatusCode)
	}
	if resp := d.request(t, http.MethodGet, "/api/agents/default/chats", "", map[string]string{"Authorization": "Bearer " + stored}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the stored token: %d", resp.StatusCode)
	}
}

func TestUserOnLoopbackNeedsNoToken(t *testing.T) {
	t.Parallel()
	// Given dex on loopback without DEX_TOKEN, then the API needs no token.
	d := startDex(t, t.TempDir(), "")
	if resp := d.request(t, http.MethodGet, "/api/agents/default/chats", "", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/agents/default/chats: %d", resp.StatusCode)
	}
}

func TestStoredTokenOfAnOlderDatabaseIsEmpty(t *testing.T) {
	t.Parallel()
	// Given a data directory with no database, and one with a schema from before tokens.
	if token, err := settings.StoredToken(t.Context(), t.TempDir()); err != nil || token != "" {
		t.Fatalf("no database: %q %v", token, err)
	}
	dir := t.TempDir()
	conn, err := sql.Open("sqlite", filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), "CREATE TABLE agents (id TEXT PRIMARY KEY) STRICT"); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	// Then the CLI reads no token and does not change the database.
	if token, err := settings.StoredToken(t.Context(), dir); err != nil || token != "" {
		t.Fatalf("older schema: %q %v", token, err)
	}
}

func TestUserUpdaterHoldsMaintenanceOnANetworkHost(t *testing.T) {
	t.Parallel()
	// Given a service on a wildcard host, which requires the stored token.
	dataDir := t.TempDir()
	d := startDexWith(t, app.Config{DataDir: dataDir, RequireToken: true})
	c := service.Config{Host: "0.0.0.0", Port: d.srv.Listener.Addr().(*net.TCPAddr).Port, DataDir: dataDir}

	// When a local client acquires and releases maintenance, then the stored token admits it.
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		req, err := service.Request(t.Context(), c, method, "/api/maintenance")
		if err != nil {
			t.Fatal(err)
		}
		resp, err := service.LoopbackClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s /api/maintenance: %d", method, resp.StatusCode)
		}
	}
}
