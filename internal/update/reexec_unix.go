//go:build darwin || linux

package update

import (
	"fmt"
	"syscall"
)

// Reexec replaces the current process image with executable, keeping the same PID and supervisor.
func Reexec(executable string, argv, env []string) error {
	if executable == "" {
		return fmt.Errorf("missing executable path for re-exec")
	}
	if len(argv) == 0 {
		argv = []string{executable}
	}
	return syscall.Exec(executable, argv, env) //nolint:gosec // G204: re-execs the verified dex binary in place.
}
