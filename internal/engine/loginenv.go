package engine

import (
	"context"
	"fmt"
	"iter"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// toolEnv is the base environment of all bots: the login shell environment, else the
// fallback keys, plus the 1Password variables of the settings. One capture serves all bots
// until an edit of a shell rc file.
type toolEnv struct {
	home     string
	fallback map[string]string
	capture  func(ctx context.Context, base map[string]string) (map[string]string, error)
	mu       sync.Mutex
	env      map[string]string
	captured bool // a capture ran, also when it failed
	rc       string
	warning  string
	op       OnePassword
	opSet    int
}

func newToolEnv(home string, fallback map[string]string, capture func(ctx context.Context, base map[string]string) (map[string]string, error)) *toolEnv {
	return &toolEnv{home: home, fallback: maps.Clone(fallback), capture: capture}
}

// rcFiles are startup files of common shells. An open or reload checks them before
// it reuses the last shell capture. An edit does not trigger a reload.
var rcFiles = []string{".zshenv", ".zprofile", ".zshrc", ".zlogin", ".bash_profile", ".bash_login", ".bashrc", ".profile", ".config/fish/config.fish"}

func (t *toolEnv) rcStamp() string {
	if t.capture == nil {
		return ""
	}
	return fileStamp(func(yield func(string) bool) {
		for _, name := range rcFiles {
			if !yield(filepath.Join(t.home, name)) {
				return
			}
		}
	})
}

// load returns the base environment for a bot that opens. It captures the login shell
// again only after an rc file change, also after a failure, so that a broken shell does not
// cost every caller the timeout. A failure keeps the last captured environment, so a slow rc
// edit does not take PATH entries away from tools.
func (t *toolEnv) load(ctx context.Context) (map[string]string, OnePassword) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if rc := t.rcStamp(); t.capture != nil && (!t.captured || rc != t.rc) {
		env, err := t.capture(ctx, maps.Clone(t.fallback))
		switch {
		case err == nil:
			t.env, t.captured, t.rc, t.warning = env, true, rc, ""
		case ctx.Err() == nil:
			t.captured, t.rc = true, rc
			if t.env != nil {
				t.warning = "the login shell failed, so tools keep the last captured environment: " + err.Error()
			} else {
				t.warning = "the login shell environment is not available, so tools get only PATH, HOME, TMPDIR, LANG, USER, and SHELL: " + err.Error()
			}
		}
	}
	return t.current(), t.op
}

// current returns the last base environment. The caller holds mu.
func (t *toolEnv) current() map[string]string {
	if t.env != nil {
		return maps.Clone(t.env)
	}
	return maps.Clone(t.fallback)
}

// snapshot returns the last base environment without a capture, for reads that must not wait
// for a shell.
func (t *toolEnv) snapshot() (map[string]string, OnePassword, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.current(), t.op, t.warning
}

// stamp changes when an rc file or the 1Password settings change.
func (t *toolEnv) stamp() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return fmt.Sprintf("%s|op%d", t.rcStamp(), t.opSet)
}

func (t *toolEnv) setOnePassword(op OnePassword) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if op == t.op {
		return false
	}
	t.op = op
	t.opSet++
	return true
}

// fileStamp joins the size and modification time of each path. A missing file has its own value.
func fileStamp(paths iter.Seq[string]) string {
	var b strings.Builder
	for path := range paths {
		if info, err := os.Stat(path); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", path, info.Size(), info.ModTime().UnixNano())
		} else {
			fmt.Fprintf(&b, "%s:-;", path)
		}
	}
	return b.String()
}
