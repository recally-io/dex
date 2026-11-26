// Package db opens control.sqlite and applies the goose migrations in migrations/.
package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

const sqlitePragmas = "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"

var sqliteHeader = []byte("SQLite format 3\x00")

//go:embed migrations/*.sql
var migrations embed.FS

// Tests open many fresh control.sqlite files in isolated temp directories. Running all historical
// goose migrations through modernc.org/sqlite under -race takes several seconds per database, so
// test binaries migrate one real SQLite template keyed by the SHA-256 of the embedded migrations
// and copy its bytes into new paths. Existing files (all upgrade and restart tests) and production
// binaries always run migrate directly.
var (
	freshTemplateMu    sync.Mutex
	freshTemplateBytes []byte
)

// Open opens the database at path and migrates it to the newest schema. dex is a local
// application with no separate deploy step, so it migrates on start.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seeded, err := seedFreshTestDB(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("seed %s: %w", path, err)
	}
	dsn := "file:" + path + sqlitePragmas
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if seeded {
		if err := db.PingContext(ctx); err != nil {
			return nil, errors.Join(fmt.Errorf("open %s: %w", path, err), db.Close())
		}
		return db, nil
	}
	if err := migrate(ctx, db); err != nil {
		return nil, errors.Join(fmt.Errorf("migrate %s: %w", path, err), db.Close())
	}
	return db, nil
}

func seedFreshTestDB(ctx context.Context, path string) (bool, error) {
	if !testing.Testing() || path == "" || path == ":memory:" || strings.Contains(path, "?") {
		return false, nil
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	dir := filepath.Dir(path)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false, nil
	}
	data, err := freshTestTemplate(ctx)
	if err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, ".control-seed-*.tmp")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		return false, errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Link(tmpPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func freshTestTemplate(ctx context.Context) ([]byte, error) {
	freshTemplateMu.Lock()
	defer freshTemplateMu.Unlock()
	if freshTemplateBytes != nil {
		return freshTemplateBytes, nil
	}
	cacheName, err := migrationsTemplateFilename()
	if err != nil {
		return nil, err
	}
	if data, ok := readCachedTemplate(cacheName); ok {
		freshTemplateBytes = data
		return freshTemplateBytes, nil
	}
	dir, err := os.MkdirTemp("", "dex-db-template-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	tmpPath := filepath.Join(dir, "control.sqlite")
	conn, err := sql.Open("sqlite", "file:"+tmpPath+sqlitePragmas)
	if err != nil {
		return nil, err
	}
	buildCtx := context.WithoutCancel(ctx)
	if err := migrate(buildCtx, conn); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	if _, err := conn.ExecContext(buildCtx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	if err := conn.Close(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	data, err := root.ReadFile("control.sqlite")
	if err != nil {
		return nil, err
	}
	writeCachedTemplate(cacheName, data)
	freshTemplateBytes = data
	return freshTemplateBytes, nil
}

func migrationsTemplateFilename() (string, error) {
	h := sha256.New()
	err := fs.WalkDir(migrations, "migrations", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		data, err := migrations.ReadFile(path)
		if err != nil {
			return err
		}
		_, _ = h.Write([]byte(path))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(data)
		_, _ = h.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", err
	}
	return "dex-test-db-" + hex.EncodeToString(h.Sum(nil))[:16] + ".sqlite", nil
}

func readCachedTemplate(name string) ([]byte, bool) {
	root, err := os.OpenRoot(os.TempDir())
	if err != nil {
		return nil, false
	}
	defer func() { _ = root.Close() }()
	data, err := root.ReadFile(name)
	if err != nil || len(data) < 4096 || !bytes.HasPrefix(data, sqliteHeader) {
		return nil, false
	}
	return data, true
}

func writeCachedTemplate(name string, data []byte) {
	tmp, err := os.CreateTemp(os.TempDir(), ".dex-test-db-*.tmp")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return
	}
	if err := os.Rename(tmpPath, filepath.Join(os.TempDir(), name)); err != nil {
		_ = os.Remove(tmpPath)
	}
}

func migrate(ctx context.Context, db *sql.DB) error {
	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	// A release branch can ship a migration with a later version than migrations that main
	// already has. Upgrading such a database must apply the lower versions too, so each
	// migration must not depend on a migration from a different branch.
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, dir, goose.WithAllowOutofOrder(true))
	if err != nil {
		return err
	}
	_, err = provider.Up(ctx)
	return err
}
