package localdex_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/localdex"
)

func TestUserOpensLocalWorkspaceWithoutAServer(t *testing.T) {
	t.Parallel()
	// Given an isolated data directory, start the runtime without a listener.
	cfg := localdex.Config{DataDir: t.TempDir(), Version: "local-test", Mobile: true}
	instance, err := localdex.Start(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := instance.Close(); err != nil {
			t.Error(err)
		}
	})

	if other, err := localdex.Start(t.Context(), cfg); err == nil {
		_ = other.Close()
		t.Fatal("a second runtime opened the same workspace")
	}

	// When the caller requests health and the workspace, both are available without sign-in.
	for _, check := range []struct{ path, contentType, body string }{
		{"/api/health", "application/json", "local-test"},
		{"/", "text/html", `id="root"`},
	} {
		w := httptest.NewRecorder()
		instance.Handler().ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, check.path, nil))
		if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), check.contentType) || !strings.Contains(w.Body.String(), check.body) {
			t.Errorf("GET %s: status %d, type %q, want %q in body", check.path, w.Code, w.Header().Get("Content-Type"), check.body)
		}
	}

	// A caller outside the native bridge cannot bypass cross-origin protection.
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/tasks", nil)
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	instance.Handler().ServeHTTP(w, request)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST: status %d, want %d", w.Code, http.StatusForbidden)
	}
}
