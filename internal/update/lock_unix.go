//go:build darwin || linux

package update

import (
	"errors"
	"os"
	"syscall"
)

func lockUpdate(path string) (func(), error) {
	//nolint:gosec // G304: lock belongs beside the user-selected executable.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}
