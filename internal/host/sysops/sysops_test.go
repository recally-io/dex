package sysops_test

import (
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/host/sysops"
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

func TestUserConcurrentBackupsProduceDistinctPrivateFiles(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	conn, err := db.Open(t.Context(), filepath.Join(dataDir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ts := time.Date(2026, 11, 23, 4, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() {
			b, err := sysops.BackupDatabase(t.Context(), conn, dataDir, "v1.0.0", ts)
			if err != nil {
				errs <- err
				return
			}
			info, statErr := os.Stat(filepath.Join(sysops.BackupsDir(dataDir), b.Name))
			if statErr != nil {
				errs <- statErr
				return
			}
			if info.Mode().Perm() != 0o600 {
				errs <- os.ErrPermission
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	listed, err := sysops.ListBackups(dataDir)
	if err != nil || len(listed) != 4 {
		t.Fatalf("expected 4 distinct backups, got %d (%v)", len(listed), err)
	}
}

func TestUserLogBufferBoundsFieldSizeAndPreservesGroupPrefixOrder(t *testing.T) {
	t.Parallel()
	buf := sysops.NewLogBuffer(10)
	logger := slog.New(sysops.TeeHandler(slog.DiscardHandler, buf)).With("top", "yes").WithGroup("sub")
	big := strings.Repeat("x", 4096)
	logger.Info(big, "k", big)

	entries := buf.Entries(10)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if len(entries[0].Message) > 2060 || len(entries[0].Attrs) > 2060 {
		t.Fatalf("entry not bounded: msg=%d attrs=%d", len(entries[0].Message), len(entries[0].Attrs))
	}
	if !strings.Contains(entries[0].Attrs, "top=yes") || strings.Contains(entries[0].Attrs, "sub.top=") || !strings.Contains(entries[0].Attrs, "sub.k=") {
		t.Fatalf("group prefix order wrong: %q", entries[0].Attrs)
	}
}
