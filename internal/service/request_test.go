package service

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserCLIReachesAServiceOnANetworkHost(t *testing.T) {
	// Given a data directory whose database holds the stored token.
	dataDir := t.TempDir()
	conn, err := db.Open(t.Context(), filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlc.New(conn).CreateAccessTokenIfMissing(t.Context(), "stored-token-0123456789abcdefghijklmnop"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// When the CLI builds a request while the database is open, then a network host gets the
	// stored token and a loopback host gets none.
	for host, want := range map[string]string{"0.0.0.0": "Bearer stored-token-0123456789abcdefghijklmnop", "127.0.0.1": ""} {
		req, err := Request(t.Context(), Config{Host: host, Port: 18787, DataDir: dataDir}, http.MethodGet, "/api/health")
		if err != nil || req.Header.Get("Authorization") != want || req.URL.Host != "127.0.0.1:18787" {
			t.Fatalf("%s: %v %q %s", host, err, req.Header.Get("Authorization"), req.URL)
		}
	}
}
