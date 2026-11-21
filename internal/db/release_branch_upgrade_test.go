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

func TestUserUpgradesFromAReleaseBranchThatSkippedMainMigrations(t *testing.T) {
	// Given a database from v0.19.1. Its release branch applied runner_workspaces, which has a
	// later version than the Task Inbox migrations that main added before it.
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	released := fstest.MapFS{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") || (name >= "20261120232000_" && name != "20261121180000_runner_workspaces.sql") {
			continue
		}
		content, err := os.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		released[name] = &fstest.MapFile{Data: content}
	}
	path := filepath.Join(t.TempDir(), "control.sqlite")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	provider, err := goose.NewProvider(goose.DialectSQLite3, old, released)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// When the new build starts, twice.
	for range 2 {
		conn, err := db.Open(t.Context(), path)
		if err != nil {
			t.Fatalf("a release-branch database must upgrade: %v", err)
		}
		// Then the skipped migrations ran, including their seed rows.
		var enabled string
		if err := conn.QueryRowContext(t.Context(), "SELECT name FROM enabled_plugins").Scan(&enabled); err != nil || enabled != "github" {
			t.Fatalf("the skipped plugin migrations must turn GitHub on: %q %v", enabled, err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
