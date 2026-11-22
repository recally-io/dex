// Package loginenv captures exports from the execution account's login shell.
package loginenv

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	marker  = "__DEX_LOGIN_ENV__"
	timeout = 10 * time.Second
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Environment captures local tool exports. The shell starts from base, not from the Dex
// process environment, so Dex settings such as DEX_TOKEN do not reach it.
func Environment(ctx context.Context, base map[string]string) (map[string]string, error) {
	environ := make([]string, 0, len(base))
	for key, value := range base {
		environ = append(environ, key+"="+value)
	}
	return captureShellEnv(ctx, cmp.Or(loginShell(ctx), "/bin/sh"), environ, timeout)
}

func captureShellEnv(ctx context.Context, shell string, environ []string, limit time.Duration) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var out bytes.Buffer
	err := probe(ctx, shell, environ, "printf '%s' "+marker+"; env -0", &out)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("login shell %s did not finish in %s", shell, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("login shell %s: %w", shell, err)
	}
	_, data, ok := bytes.Cut(out.Bytes(), []byte(marker))
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

// Path captures only the exported PATH. Its errors are fixed diagnostic categories and
// never contain the shell path, profile output, or any environment values.
func Path(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	shell := loginShell(ctx)
	if ctx.Err() != nil {
		return "", probeContextError(ctx)
	}
	if shell == "" {
		return "", errors.New("account shell unavailable")
	}
	if name := filepath.Base(shell); name == "nologin" || name == "false" {
		return "", errors.New("login disabled")
	}
	// Preserve the startup file locations for Zsh and Fish without expanding tool defaults.
	keys := [...]string{"PATH", "HOME", "TMPDIR", "LANG", "USER", "SHELL", "ZDOTDIR", "XDG_CONFIG_HOME"}
	// A non-nil environment prevents os/exec from supplying the process environment.
	environ := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			environ = append(environ, key+"="+value)
		}
	}
	var out pathOutput
	// The absolute printenv works even when the profile sets PATH to only custom dirs.
	// NUL ends the record before any output from logout hooks.
	err := probe(ctx, shell, environ, `printf '%s' `+marker+`; /usr/bin/printenv PATH; printf '\000'`, &out)
	if ctx.Err() != nil {
		return "", probeContextError(ctx)
	}
	if out.overflow {
		return "", errors.New("profile output limit")
	}
	if _, ok := errors.AsType[*exec.ExitError](err); ok {
		return "", errors.New("login shell exited unsuccessfully")
	}
	if err != nil {
		return "", errors.New("login shell could not run")
	}
	if _, data, marked := bytes.Cut(out.buf.Bytes(), []byte(marker)); marked {
		if record, _, ended := bytes.Cut(data, []byte{0}); ended {
			if path, newline := bytes.CutSuffix(record, []byte{'\n'}); newline && len(path) > 0 {
				return string(path), nil
			}
		}
	}
	return "", errors.New("no exported PATH")
}

func probeContextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New("probe timed out")
	}
	return errors.New("probe canceled")
}

type pathOutput struct {
	buf      bytes.Buffer
	overflow bool
}

func (b *pathOutput) Write(data []byte) (int, error) {
	const limit = 64 << 10
	n := min(len(data), limit-b.buf.Len())
	_, _ = b.buf.Write(data[:n])
	b.overflow = b.overflow || n < len(data)
	return len(data), nil
}

func probe(ctx context.Context, shell string, environ []string, command string, stdout io.Writer) error {
	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c", command) //nolint:gosec // The shell is the user's own login shell.
	cmd.Env = environ
	// A new session has no controlling terminal. On timeout, stop the process group,
	// including jobs that still share the shell's group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	cmd.Stdout = stdout
	// A profile's background job can keep stdout open after the shell exits. Keep any
	// complete output when only the wait for the pipe failed.
	if err := cmd.Run(); !errors.Is(err, exec.ErrWaitDelay) {
		return err
	}
	return nil
}

// loginShell uses SHELL when set, else looks up the current user's account shell.
func loginShell(ctx context.Context) string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	u, err := user.Current()
	if err != nil {
		return ""
	}
	if runtime.GOOS == "darwin" {
		out, err := accountShellOutput(ctx, "dscl", ".", "-read", "/Users/"+u.Username, "UserShell")
		if _, shell, ok := strings.Cut(strings.TrimSpace(string(out)), "UserShell: "); err == nil && ok {
			return shell
		}
	} else {
		out, err := accountShellOutput(ctx, "getent", "passwd", u.Uid)
		if fields := strings.Split(strings.TrimSpace(string(out)), ":"); err == nil && len(fields) == 7 {
			return fields[6]
		}
	}
	return ""
}

func accountShellOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // The commands query only the current account.
	cmd.WaitDelay = time.Second
	return cmd.Output()
}
