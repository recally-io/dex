package runnertest

import (
	"os"
	"path/filepath"
	"testing"
)

// allowSkillMirrorCleanup makes read-only mirror directories removable from
// a simulated remote host on test exit.
func allowSkillMirrorCleanup(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || !entry.IsDir() {
				return err
			}
			if entry.Name() != "skills" || filepath.Base(filepath.Dir(path)) != ".dex" {
				return nil
			}
			return filepath.WalkDir(path, func(dir string, entry os.DirEntry, err error) error {
				if err == nil && entry.IsDir() {
					return os.Chmod(dir, 0o700) //nolint:gosec // Test directories need owner execute permission for cleanup.
				}
				return err
			})
		}); err != nil && !os.IsNotExist(err) {
			t.Errorf("make skill mirrors removable: %v", err)
		}
	})
}
