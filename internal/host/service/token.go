package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/recally-io/dex/internal/db/sqlc"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// StoredToken reads the access token of the data directory without a migration. It returns ""
// when dex has not created one: the database does not exist, or its schema is older.
func StoredToken(ctx context.Context, dataDir string) (string, error) {
	path := filepath.Join(dataDir, "control.sqlite")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	// Read-only and without migrations, so the CLI can read while a server of another version
	// runs. The URI is escaped, because SQLite reads "?" and "#" in a path as URI syntax. Not
	// immutable: the server writes the file at the same time.
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}
	conn, err := sql.Open("sqlite", u.String())
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer conn.Close() //nolint:errcheck // a read-only connection has nothing to flush
	var tables int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'access_token'").Scan(&tables); err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if tables == 0 {
		return "", nil
	}
	token, err := sqlc.New(conn).GetAccessToken(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the access token: %w", err)
	}
	return token, nil
}
