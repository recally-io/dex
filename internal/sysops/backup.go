package sysops

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const maxRetainedBackups = 20

var backupMu sync.Mutex

// BackupInfo describes one control.sqlite backup file in <dataDir>/backups/.
type BackupInfo struct {
	Name      string
	SizeBytes int64
	CreatedAt int64
}

// BackupsDir returns the backups directory under dataDir.
func BackupsDir(dataDir string) string {
	return filepath.Join(dataDir, "backups")
}

// BackupDatabase writes a consistent, WAL-safe snapshot of db into <dataDir>/backups/.
// It stages inside a private 0700 temp directory before moving into place so no world-readable
// window or concurrent filename race can occur.
func BackupDatabase(ctx context.Context, db *sql.DB, dataDir, version string, now time.Time) (BackupInfo, error) {
	if db == nil {
		return BackupInfo{}, errors.New("database is unavailable")
	}
	dir := BackupsDir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return BackupInfo{}, fmt.Errorf("create backups directory: %w", err)
	}
	tmpDir, err := os.MkdirTemp(dir, ".backup-*")
	if err != nil {
		return BackupInfo{}, fmt.Errorf("create backup staging dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	staged := filepath.Join(tmpDir, "control.sqlite")
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", staged); err != nil {
		return BackupInfo{}, fmt.Errorf("back up control.sqlite: %w", err)
	}
	if err := os.Chmod(staged, 0o600); err != nil {
		return BackupInfo{}, fmt.Errorf("set backup permissions: %w", err)
	}

	backupMu.Lock()
	defer backupMu.Unlock()
	tag := sanitizeTag(version)
	base := fmt.Sprintf("control-%s-%s", tag, now.UTC().Format("20060102-150405"))
	name := base + ".sqlite"
	dest := filepath.Join(dir, name)
	for i := 1; i < 1000; i++ {
		if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("%s-%d.sqlite", base, i)
		dest = filepath.Join(dir, name)
	}
	if err := os.Rename(staged, dest); err != nil {
		return BackupInfo{}, fmt.Errorf("finalize backup: %w", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		return BackupInfo{}, err
	}
	pruneBackups(dir, maxRetainedBackups)
	return BackupInfo{
		Name:      name,
		SizeBytes: info.Size(),
		CreatedAt: info.ModTime().UnixMilli(),
	}, nil
}

// ListBackups returns backups in <dataDir>/backups/, newest first.
func ListBackups(dataDir string) ([]BackupInfo, error) {
	dir := BackupsDir(dataDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []BackupInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]BackupInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sqlite") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupInfo{
			Name:      entry.Name(),
			SizeBytes: info.Size(),
			CreatedAt: info.ModTime().UnixMilli(),
		})
	}
	slices.SortFunc(out, func(a, b BackupInfo) int {
		if c := cmp.Compare(b.CreatedAt, a.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(b.Name, a.Name)
	})
	return out, nil
}

func pruneBackups(dir string, keep int) {
	backups, err := ListBackups(filepath.Dir(dir))
	if err != nil || len(backups) <= keep {
		return
	}
	for _, old := range backups[keep:] {
		_ = os.Remove(filepath.Join(dir, old.Name))
	}
}

func sanitizeTag(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "dev"
	}
	var b strings.Builder
	for _, r := range version {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "dev"
	}
	return b.String()
}
