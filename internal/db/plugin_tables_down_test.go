package db_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/recally-io/dex/internal/db"
)

func TestOwnerRollsBackPluginTablesOnlyBeforeAPluginMigrates(t *testing.T) {
	// Given a database at the core migration that adds enabled_plugins and applied_plugin_migrations.
	path := filepath.Join(t.TempDir(), "control.sqlite")
	conn, err := db.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	provider, err := goose.NewProvider(goose.DialectSQLite3, raw, os.DirFS("migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), "INSERT INTO enabled_plugins (name) VALUES ('webhook')"); err != nil {
		t.Fatal(err)
	}

	// When no plugin has migrated, a rollback keeps the choices.
	if _, err := provider.DownTo(t.Context(), 20261121180000); err != nil {
		t.Fatalf("a rollback before any plugin migration must work: %v", err)
	}
	var enabled string
	if err := raw.QueryRowContext(t.Context(), "SELECT group_concat(name) FROM (SELECT name FROM plugin_settings WHERE enabled = 1 ORDER BY name)").Scan(&enabled); err != nil || enabled != "github,webhook" {
		t.Fatalf("the rollback must keep the plugin choices: %q %v", enabled, err)
	}

	// When a plugin has migrated, the rollback fails and changes nothing.
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(t.Context(), "INSERT INTO applied_plugin_migrations (plugin, version, applied_at) VALUES ('github', 0, 1), ('github', 1, 1)"); err != nil {
		t.Fatal(err)
	}
	_, err = provider.DownTo(t.Context(), 20261121180000)
	if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("a rollback after a plugin migration must fail: %v", err)
	}
	var versions int
	if err := raw.QueryRowContext(t.Context(), "SELECT count(*) FROM applied_plugin_migrations").Scan(&versions); err != nil || versions != 2 {
		t.Fatalf("a refused rollback must keep the plugin versions: %d %v", versions, err)
	}
}
