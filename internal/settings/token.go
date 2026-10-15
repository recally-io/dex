package settings

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// CheckToken accepts 32 to 256 base64url characters. The length is a floor, not proof of entropy:
// a user token must be random, because the API does not limit sign-in attempts.
func CheckToken(token string) error {
	if len(token) < 32 || len(token) > 256 || strings.Trim(token, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
		return errors.New("the token must be 32 to 256 characters from A-Z, a-z, 0-9, '-' and '_'; generate one with: openssl rand -base64 32 | tr '+/' '-_' | tr -d '='")
	}
	return nil
}

// AccessToken returns the stored access token. The first call creates it; later calls never
// replace it.
func (s *Service) AccessToken(ctx context.Context) (string, error) {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails; it crashes the program instead
	err := s.q.CreateAccessTokenIfMissing(ctx, base64.RawURLEncoding.EncodeToString(b))
	if err != nil {
		return "", fmt.Errorf("create the access token: %w", err)
	}
	token, err := s.q.GetAccessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("read the access token: %w", err)
	}
	return token, nil
}

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
