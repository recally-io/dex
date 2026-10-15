// Package db opens control.sqlite and applies the goose migrations in migrations/.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open opens the database at path and migrates it to the newest schema. dex is a local
// application with no separate deploy step, so it migrates on start.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := migrate(ctx, db); err != nil {
		return nil, errors.Join(fmt.Errorf("migrate %s: %w", path, err), db.Close())
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, dir)
	if err != nil {
		return err
	}
	_, err = provider.Up(ctx)
	return err
}
