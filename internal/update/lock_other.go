//go:build !darwin && !linux

package update

import (
	"fmt"
	"runtime"
)

func lockUpdate(string) (func(), error) { return nil, fmt.Errorf("not supported on %s", runtime.GOOS) }
