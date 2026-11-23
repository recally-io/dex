package engine

import (
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/db"
)

func testMCPStore(t *testing.T, data string) *MCPStore {
	t.Helper()
	conn, err := db.Open(t.Context(), filepath.Join(data, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return NewMCPStore(conn)
}

func testSetMCP(t *testing.T, store *MCPStore, agentID, name string, config serverConfig) {
	t.Helper()
	if err := store.set(t.Context(), agentID, name, config); err != nil {
		t.Fatal(err)
	}
}
