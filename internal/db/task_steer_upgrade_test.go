package db_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
	"github.com/recally-io/dex/internal/db"
)

func TestUserUpgradesQuestionDatabaseWithTaskSteer(t *testing.T) {
	// Given the question schema before task steering. Later migrations must not enter this fixture.
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	mainSchema := fstest.MapFS{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") || name >= "20261110140000_" || strings.HasSuffix(name, "_task_steer_receipts.sql") {
			continue
		}
		data, err := os.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		mainSchema[name] = &fstest.MapFile{Data: data}
	}
	path := filepath.Join(t.TempDir(), "control.sqlite")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	provider, err := goose.NewProvider(goose.DialectSQLite3, old, mainSchema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// When dex upgrades with its normal migration policy.
	conn, err := db.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("upgrade from main: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Then startup succeeds and the new receipt table is available.
	var count int
	if err := conn.QueryRowContext(t.Context(), "SELECT count(*) FROM task_steer_receipts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("new receipts: %d, want 0", count)
	}
}
