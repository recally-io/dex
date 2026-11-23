package sysops_test

import (
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/sysops"
)

func TestUserLogBufferKeepsRecentEntriesInOrder(t *testing.T) {
	t.Parallel()
	// Given a small ring buffer attached to a logger.
	buf := sysops.NewLogBuffer(3)
	logger := slog.New(sysops.TeeHandler(slog.DiscardHandler, buf))

	// When more records are logged than the ring capacity.
	logger.Info("first", "n", 1)
	logger.Warn("second", "n", 2)
	logger.Error("third", "n", 3)
	logger.Info("fourth", "n", 4)

	// Then only the newest three entries remain in chronological order.
	entries := buf.Entries(10)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	want := []string{"second", "third", "fourth"}
	for i, msg := range want {
		if entries[i].Message != msg {
			t.Fatalf("entry[%d] = %q, want %q", i, entries[i].Message, msg)
		}
	}
	if !strings.Contains(entries[2].Attrs, "n=4") {
		t.Fatalf("expected attrs to include n=4, got %q", entries[2].Attrs)
	}
}

func TestUserDatabaseBackupCreatesReadableSQLiteCopy(t *testing.T) {
	t.Parallel()
	// Given a live control.sqlite database with applied migrations.
	dataDir := t.TempDir()
	conn, err := db.Open(t.Context(), filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// When the user triggers a hot database backup.
	backup, err := sysops.BackupDatabase(t.Context(), conn, dataDir, "v0.20.0-rc.1", time.Date(2026, 11, 23, 4, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}

	// Then the backup file exists, is listed, and opens as a valid SQLite copy.
	if backup.SizeBytes <= 0 || !strings.HasPrefix(backup.Name, "control-v0.20.0-rc.1-") {
		t.Fatalf("unexpected backup metadata: %+v", backup)
	}
	listed, err := sysops.ListBackups(dataDir)
	if err != nil || len(listed) != 1 || listed[0].Name != backup.Name {
		t.Fatalf("ListBackups = %+v, %v", listed, err)
	}
	copyConn, err := sql.Open("sqlite", "file:"+filepath.Join(sysops.BackupsDir(dataDir), backup.Name)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer copyConn.Close()
	var channel string
	if err := copyConn.QueryRowContext(t.Context(), "SELECT update_channel FROM system_settings WHERE id = 1").Scan(&channel); err != nil || channel != "stable" {
		t.Fatalf("backup DB read failed: %q %v", channel, err)
	}
}
