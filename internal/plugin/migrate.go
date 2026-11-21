package plugin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

// Migrate applies the migrations/*.sql files of one plugin. dex calls it at start, only for an
// enabled plugin, after the core migrations. Each plugin has its own versions (00001, 00002, ...),
// which do not mix with the core timeline. All plugins record them in the one core table
// applied_plugin_migrations. Each plugin table is named plugin_<name>_<table>, and only plugin tables can
// refer to core tables.
func Migrate(ctx context.Context, db *sql.DB, name string, files fs.FS) error {
	dir, err := fs.Sub(files, "migrations")
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectCustom, db, dir, goose.WithStore(versions{plugin: name}), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return fmt.Errorf("plugin %s migrations: %w", name, err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("plugin %s migrations: %w", name, err)
	}
	return nil
}

// versions is the goose version table of one plugin: its rows in applied_plugin_migrations. A core
// migration creates that table, so the version table of a plugin "exists" when goose has stored
// its version 0.
type versions struct{ plugin string }

var _ database.StoreExtender = versions{}

func (versions) Tablename() string { return "applied_plugin_migrations" }

func (versions) CreateVersionTable(context.Context, database.DBTxConn) error { return nil }

func (v versions) TableExists(ctx context.Context, db database.DBTxConn) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM applied_plugin_migrations WHERE plugin = ? AND version = 0)", v.plugin).Scan(&exists)
	return exists, err
}

func (v versions) Insert(ctx context.Context, db database.DBTxConn, req database.InsertRequest) error {
	_, err := db.ExecContext(ctx, "INSERT INTO applied_plugin_migrations (plugin, version, applied_at) VALUES (?, ?, ?)", v.plugin, req.Version, time.Now().UnixMilli())
	return err
}

func (v versions) Delete(ctx context.Context, db database.DBTxConn, version int64) error {
	_, err := db.ExecContext(ctx, "DELETE FROM applied_plugin_migrations WHERE plugin = ? AND version = ?", v.plugin, version)
	return err
}

func (v versions) GetMigration(ctx context.Context, db database.DBTxConn, version int64) (*database.GetMigrationResult, error) {
	var at int64
	err := db.QueryRowContext(ctx, "SELECT applied_at FROM applied_plugin_migrations WHERE plugin = ? AND version = ?", v.plugin, version).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, database.ErrVersionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &database.GetMigrationResult{Timestamp: time.UnixMilli(at), IsApplied: true}, nil
}

func (v versions) GetLatestVersion(ctx context.Context, db database.DBTxConn) (int64, error) {
	var latest sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT max(version) FROM applied_plugin_migrations WHERE plugin = ?", v.plugin).Scan(&latest); err != nil {
		return 0, err
	}
	if !latest.Valid {
		return 0, database.ErrVersionNotFound
	}
	return latest.Int64, nil
}

func (v versions) ListMigrations(ctx context.Context, db database.DBTxConn) ([]*database.ListMigrationsResult, error) {
	rows, err := db.QueryContext(ctx, "SELECT version FROM applied_plugin_migrations WHERE plugin = ? ORDER BY version DESC", v.plugin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*database.ListMigrationsResult
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		out = append(out, &database.ListMigrationsResult{Version: version, IsApplied: true})
	}
	return out, rows.Err()
}
