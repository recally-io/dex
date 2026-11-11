package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// loginEnvMarker separates rc file output from the environment that env -0 prints.
const loginEnvMarker = "__DEX_LOGIN_ENV__"

// LoginShellEnv returns the environment of the user's interactive login shell, so tools see
// what the user exports in rc files. The shell starts from base, not from the dex process
// environment, so a dex setting such as DEX_TOKEN does not reach tools.
func LoginShellEnv(ctx context.Context, base map[string]string) (map[string]string, error) {
	environ := make([]string, 0, len(base))
	for key, value := range base {
		environ = append(environ, key+"="+value)
	}
	return captureShellEnv(ctx, loginShell(ctx), environ, 10*time.Second)
}

func captureShellEnv(ctx context.Context, shell string, environ []string, timeout time.Duration) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c", "printf '%s' "+loginEnvMarker+"; env -0") //nolint:gosec // The shell is the user's own login shell.
	cmd.Env = environ
	// A new session has no controlling terminal, so an interactive shell cannot take the
	// terminal of a foreground dex. Its process group stops as a whole on timeout, also with
	// background jobs that an rc file starts.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("login shell %s did not finish in %s", shell, timeout)
	}
	// A background job of an rc file can keep stdout open after the shell exits. The output is
	// complete when the shell exited, so only the wait for the pipe failed.
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		return nil, fmt.Errorf("login shell %s: %w", shell, err)
	}
	_, data, ok := bytes.Cut(out.Bytes(), []byte(loginEnvMarker))
	if !ok {
		return nil, fmt.Errorf("login shell %s printed no environment", shell)
	}
	env := map[string]string{}
	for item := range bytes.SplitSeq(data, []byte{0}) {
		if key, value, ok := strings.Cut(string(item), "="); ok && envName.MatchString(key) {
			env[key] = value
		}
	}
	if len(env) == 0 {
		return nil, fmt.Errorf("login shell %s printed no environment", shell)
	}
	return env, nil
}

// loginShell is $SHELL, else the shell of the user's account, for a service manager that
// does not set SHELL.
func loginShell(ctx context.Context) string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	u, err := user.Current()
	if err != nil {
		return "/bin/sh"
	}
	var out []byte
	if runtime.GOOS == "darwin" {
		out, err = exec.CommandContext(ctx, "dscl", ".", "-read", "/Users/"+u.Username, "UserShell").Output() //nolint:gosec // The name comes from the current user account.
		if _, shell, ok := strings.Cut(strings.TrimSpace(string(out)), "UserShell: "); err == nil && ok {
			return shell
		}
	} else {
		out, err = exec.CommandContext(ctx, "getent", "passwd", u.Uid).Output() //nolint:gosec // The UID comes from the current user account.
		if fields := strings.Split(strings.TrimSpace(string(out)), ":"); err == nil && len(fields) == 7 && fields[6] != "" {
			return fields[6]
		}
	}
	return "/bin/sh"
}

// toolEnv is the base environment of all agents: the login shell environment, else the
// fallback keys, plus the 1Password variables of the settings. One capture serves all agents
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

// rcFiles are the startup files of common shells. A change to one of them makes open agents
// reload. A file that an rc file sources is not watched; touch an rc file to apply it.
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

// load returns the base environment for an agent that opens. It captures the login shell
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

func (t *toolEnv) setOnePassword(op OnePassword) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if op != t.op {
		t.op = op
		t.opSet++
	}
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
