package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func (m Manager) updatePath() string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(filepath.Dir(m.Definition()), label+".update.plist")
	}
	return filepath.Join(filepath.Dir(m.Definition()), "dex-update.service")
}
func (m Manager) installUpdate(ctx context.Context, c Config) error {
	if err := m.removeUpdate(ctx); err != nil {
		return err
	}
	if !c.AutoUpdate {
		return nil
	}
	if runtime.GOOS == "darwin" {
		data := launchPlist(label+".update", []string{c.Executable, "update", "--scheduled"}, c.Path, m.home, m.LogPath(), "", true)
		if err := writeDefinition(m.updatePath(), data); err != nil {
			return err
		}
		if err := command(ctx, "launchctl", "enable", m.target()+".update"); err != nil {
			return err
		}
		return m.bootstrap(ctx, m.updatePath())
	}
	if err := writeDefinition(m.updatePath(), updateUnit(c)); err != nil {
		return err
	}
	timer := filepath.Join(filepath.Dir(m.Definition()), "dex-update.timer")
	if err := writeDefinition(timer, []byte(updateTimer)); err != nil {
		return err
	}
	if err := command(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	return command(ctx, "systemctl", "--user", "enable", "--now", "dex-update.timer")
}
func (m Manager) removeUpdate(ctx context.Context) error {
	if runtime.GOOS == "darwin" {
		err := command(ctx, "launchctl", "print", m.target()+".update")
		if err == nil {
			if err := command(ctx, "launchctl", "bootout", m.target()+".update"); err != nil {
				return err
			}
		} else if e, ok := errors.AsType[*exec.ExitError](err); !ok || e.ExitCode() != 113 {
			return err
		}
	} else {
		timer := filepath.Join(filepath.Dir(m.Definition()), "dex-update.timer")
		if _, err := os.Stat(timer); err == nil {
			if err := command(ctx, "systemctl", "--user", "disable", "--now", "dex-update.timer"); err != nil {
				return err
			}
			if err := command(ctx, "systemctl", "--user", "stop", "dex-update.service"); err != nil {
				return err
			}
			if err := os.Remove(timer); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Remove(m.updatePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
