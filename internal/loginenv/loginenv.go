// Package loginenv captures exports from the execution account's login shell.
package loginenv

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	marker        = "__DEX_LOGIN_ENV__"
	timeout       = 10 * time.Second
	pathOutputMax = 64 << 10
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Environment captures local tool exports. The shell starts from base, not from the Dex
// process environment, so Dex settings such as DEX_TOKEN do not reach it.
func Environment(ctx context.Context, base map[string]string) (map[string]string, error) {
	environ := make([]string, 0, len(base))
	for key, value := range base {
		environ = append(environ, key+"="+value)
	}
	return captureShellEnv(ctx, environ, timeout, false)
}

// Path keeps only the exported PATH. The probe never logs shell paths, profile
// output, or environment values.
func Path(ctx context.Context) (string, error) {
	// Preserve the startup file locations for Zsh and Fish without expanding tool defaults.
	keys := [...]string{"PATH", "HOME", "TMPDIR", "LANG", "USER", "SHELL", "ZDOTDIR", "XDG_CONFIG_HOME"}
	// A non-nil environment prevents os/exec from supplying the process environment.
	environ := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			environ = append(environ, key+"="+value)
		}
	}
	env, err := captureShellEnv(ctx, environ, timeout, true)
	return env["PATH"], err
}

func captureShellEnv(ctx context.Context, environ []string, limit time.Duration, pathOnly bool) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	shell := loginShell(ctx)
	if shell == "" && !pathOnly {
		shell = "/bin/sh" // Keep the Local default when the account shell is unknown.
	}
	// Use an absolute capture command: the profile may replace PATH entirely.
	const command = "printf '%s' " + marker + "; /usr/bin/env -0"
	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c", command) //nolint:gosec // The shell is the user's own login shell.
	cmd.Env = environ
	// A new session has no controlling terminal. On timeout, stop the process group,
	// including jobs that still share the shell's group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var out cappedOutput
	cmd.Stdout = &out.buf
	if pathOnly {
		cmd.Stdout = &out
	}
	err := cmd.Run()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, errors.New("probe timed out")
	case ctx.Err() != nil:
		return nil, errors.New("probe canceled")
	case out.overflow:
		return nil, errors.New("profile output limit")
	case err != nil && !errors.Is(err, exec.ErrWaitDelay):
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil, errors.New("login shell exited unsuccessfully")
		}
		return nil, errors.New("login shell could not run")
	}

	// A background job can keep stdout open after the shell exits. Keep only
	// complete records, ignoring output before the marker or after the last NUL.
	_, data, _ := bytes.Cut(out.buf.Bytes(), []byte(marker))
	data, _, ended := bytes.CutLast(data, []byte{0})
	if !ended {
		data = nil
	}
	env := map[string]string{}
	for item := range bytes.SplitSeq(data, []byte{0}) {
		if key, value, ok := bytes.Cut(item, []byte{'='}); ok &&
			(!pathOnly || string(key) == "PATH") && envName.Match(key) {
			env[string(key)] = string(value)
		}
	}
	if pathOnly && env["PATH"] == "" {
		return nil, errors.New("no exported PATH")
	}
	if len(env) == 0 {
		return nil, errors.New("login shell printed no environment")
	}
	return env, nil
}

// cappedOutput bounds Runner stdout while continuing to drain the pipe.
type cappedOutput struct {
	buf      bytes.Buffer
	overflow bool
}

func (b *cappedOutput) Write(data []byte) (int, error) {
	n := min(len(data), pathOutputMax-b.buf.Len())
	b.overflow = b.overflow || n < len(data)
	_, _ = b.buf.Write(data[:n])
	return len(data), nil
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
