package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/server"
)

func TestUserDownloadsTheInstallerForTheirDexBuild(t *testing.T) {
	for _, tc := range []struct{ build, tag string }{
		{"dev", "v0.0.0-dev"},
		{"v1.2.3-rc.1", "v1.2.3-rc.1"},
	} {
		t.Run(tc.build, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/install-machine.sh", nil)
			res := httptest.NewRecorder()
			server.New(server.Config{Version: tc.build}).ServeHTTP(res, req)
			if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "version='"+tc.tag+"'") || strings.Contains(res.Body.String(), "/releases/latest") {
				t.Fatalf("installer for %s did not select %s (HTTP %d)", tc.build, tc.tag, res.Code)
			}
		})
	}
}
