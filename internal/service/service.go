// Package service manages per-user launchd and systemd services.
package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const label = "io.recally.dex"

// Manager uses the current user's service domain and definition directory.
type Manager struct{ home, config string }

func New() (Manager, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return Manager{}, fmt.Errorf("not supported on %s", runtime.GOOS)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Manager{}, err
	}
	config := cmp.Or(os.Getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config"))
	if !filepath.IsAbs(config) {
		return Manager{}, errors.New("XDG_CONFIG_HOME must be absolute")
	}
	return Manager{home: home, config: config}, nil
}
func (m Manager) Definition() string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(m.home, "Library", "LaunchAgents", label+".plist")
	}
	return filepath.Join(m.config, "systemd", "user", "dex.service")
}
func (m Manager) LogPath() string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(m.home, "Library", "Logs", "dex", "dex.log")
	}
	return "journalctl --user -u dex"
}
func (m Manager) domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }
func (m Manager) target() string { return m.domain() + "/" + label }
func (m Manager) Read() (Config, error) {
	data, err := os.ReadFile(m.Definition())
	if err != nil {
		return Config{}, err
	}
	return readConfig(data)
}

// Executable retains a stable PATH symlink when it points at this executable.
func Executable() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	for _, dir := range []string{os.TempDir(), "/tmp", "/var/tmp"} {
		temp, err := filepath.EvalSymlinks(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		if rel, err := filepath.Rel(temp, resolved); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return "", errors.New("cannot install dex from a temporary build; copy the built binary to a permanent location first")
		}
	}
	if candidate, err := exec.LookPath("dex"); err == nil {
		if target, err := filepath.EvalSymlinks(candidate); err == nil && target == resolved {
			return filepath.Abs(candidate)
		}
	}
	return resolved, nil
}

func command(ctx context.Context, name string, args ...string) error {
	//nolint:gosec // G204: fixed service-manager commands, with arguments and no shell.
	b, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(b)))
	}
	return nil
}
func (m Manager) loaded(ctx context.Context) (bool, error) {
	err := command(ctx, "launchctl", "print", m.target())
	if err == nil {
		return true, nil
	}
	if e, ok := errors.AsType[*exec.ExitError](err); ok && e.ExitCode() == 113 {
		return false, nil
	}
	return false, err
}

// Install replaces the definition and starts it. The caller checks credentials first.
func (m Manager) Install(ctx context.Context, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := m.Stop(ctx); err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		if err := os.MkdirAll(filepath.Dir(m.LogPath()), 0700); err != nil {
			return err
		}
	}
	data := unit(c)
	if runtime.GOOS == "darwin" {
		data = plist(c, m.home, m.LogPath())
	}
	if err := writeDefinition(m.Definition(), data); err != nil {
		return err
	}
	if err := m.installUpdate(ctx, c); err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		if err := command(ctx, "launchctl", "enable", m.target()); err != nil {
			return err
		}
	} else {
		if err := command(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		if err := command(ctx, "systemctl", "--user", "enable", "dex.service"); err != nil {
			return err
		}
	}
	return m.Start(ctx)
}
func writeDefinition(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// Start bootstraps the saved definition. Repeated starts leave a running service alone.
func (m Manager) Start(ctx context.Context) error {
	if runtime.GOOS == "linux" {
		// Explicit starts must recover from a previous start-limit hit.
		if err := command(ctx, "systemctl", "--user", "reset-failed", "dex.service"); err != nil {
			return err
		}
		return command(ctx, "systemctl", "--user", "start", "dex.service")
	}
	loaded, err := m.loaded(ctx)
	if err != nil {
		return err
	}
	if loaded {
		// A clean exit leaves the job registered. Without -k, kickstart leaves a running job alone.
		return command(ctx, "launchctl", "kickstart", m.target())
	}
	return m.bootstrap(ctx, m.Definition())
}
func (m Manager) bootstrap(ctx context.Context, path string) error {
	// bootout returns before launchd finishes removing the old job.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return poll(ctx, 100*time.Millisecond, func() (bool, error) {
		err := command(ctx, "launchctl", "bootstrap", m.domain(), path)
		exit, ok := errors.AsType[*exec.ExitError](err)
		return err == nil || !ok || exit.ExitCode() != 5, err
	})
}

// Stop keeps the definition for a later start and suppresses automatic restarts.
func (m Manager) Stop(ctx context.Context) error {
	if runtime.GOOS == "darwin" {
		loaded, err := m.loaded(ctx)
		if err != nil || !loaded {
			return err
		}
		if err := command(ctx, "launchctl", "bootout", m.target()); err != nil {
			return err
		}
		// A job can remain visible while launchd closes it. Start must not reuse that job.
		stopped, cancel := context.WithTimeout(ctx, 65*time.Second)
		defer cancel()
		return poll(stopped, 100*time.Millisecond, func() (bool, error) {
			loaded, err := m.loaded(stopped)
			return !loaded || err != nil, err
		})
	}
	b, err := exec.CommandContext(ctx, "systemctl", "--user", "show", "dex.service", "-p", "LoadState", "--value").Output()
	if err != nil {
		return fmt.Errorf("query user service: %w", err)
	}
	if state := strings.TrimSpace(string(b)); state == "not-found" || state == "bad-setting" {
		return nil
	}
	return command(ctx, "systemctl", "--user", "stop", "dex.service")
}
func (m Manager) Restart(ctx context.Context) error {
	if runtime.GOOS == "linux" {
		if err := command(ctx, "systemctl", "--user", "reset-failed", "dex.service"); err != nil {
			return err
		}
		return command(ctx, "systemctl", "--user", "restart", "dex.service")
	}
	if err := m.Stop(ctx); err != nil {
		return err
	}
	return m.Start(ctx)
}
func (m Manager) Uninstall(ctx context.Context) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		if err := command(ctx, "launchctl", "disable", m.target()); err != nil {
			return err
		}
	} else {
		if _, err := os.Stat(m.Definition()); err == nil {
			if err := command(ctx, "systemctl", "--user", "disable", "dex.service"); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := m.removeUpdate(ctx); err != nil {
		return err
	}
	if err := os.Remove(m.Definition()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if runtime.GOOS == "linux" {
		return command(ctx, "systemctl", "--user", "daemon-reload")
	}
	return nil
}

// poll retries until check completes or the context expires, retaining the last error.
func poll(ctx context.Context, interval time.Duration, check func() (bool, error)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		done, err := check()
		if done {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), err)
		case <-ticker.C:
		}
	}
}
