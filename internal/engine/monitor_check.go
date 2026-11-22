package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"time"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	"github.com/recally-io/dex/internal/execenv"
)

const (
	MonitorTimeout      = 20 * time.Second
	MonitorTerminalExit = 10
	monitorOutputLimit  = 16 << 10
)

// MonitorCheck uses the same environment, directory rules, shell, and variables as Bash.
// It does not admit a model turn and it returns no raw output or diagnostics to control.
func (a *Agents) MonitorCheck(ctx context.Context, ref Ref, cwd, command string, verify func(context.Context) error) (digest string, terminal bool, err error) {
	if ref.Scope != "" || ref.ID <= 0 {
		return "", false, ErrInvalidInput
	}
	if err := a.begin(); err != nil {
		return "", false, err
	}
	defer a.end()
	if _, err := a.conversation(ctx, ref); err != nil {
		return "", false, err
	}
	envs := a.environments(ref.AgentID)
	if envs == nil {
		return "", false, ErrClosed
	}
	target, err := envs.session(ctx, ref.ID)
	if err != nil {
		return "", false, execenv.ErrUnavailable
	}
	if execenv.LeaseExpired(target.Handle) {
		return "", false, execenv.ErrUnavailable
	}
	exec, err := target.executionEnv(ctx, cwd)
	if err != nil {
		return "", false, execenv.ErrUnavailable
	}
	if err := verify(ctx); err != nil {
		return "", false, err
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if command == "" {
		return "", false, nil
	}

	hash := sha256.New()
	remaining := monitorOutputLimit
	overflow := false
	timeout := MonitorTimeout.Seconds()
	result, err := exec.Exec(ctx, command, &durableenv.ShellExecOptions{
		Cwd: exec.Cwd(), Env: maps.Clone(target.Vars), InheritEnv: &target.Inherit, Timeout: &timeout,
		Window: &durableenv.ShellOutputWindow{MaxBytes: monitorOutputLimit + 1},
		OnOutput: func(_ context.Context, text string, info durableenv.ShellOutputInfo) {
			if overflow {
				return
			}
			if len(text) > remaining || info.Skipped != nil {
				overflow = true
				return
			}
			_, _ = hash.Write([]byte(text))
			remaining -= len(text)
		},
	})
	if err != nil {
		if ctx.Err() != nil {
			return "", false, ctx.Err()
		}
		if execution, ok := errors.AsType[*durableenv.ExecutionError](err); ok && execution.Code == durableenv.ExecutionErrorTimeout {
			return "", false, context.DeadlineExceeded
		}
		return "", false, errors.New("check could not complete in the target environment")
	}
	if result.ExitCode != 0 && result.ExitCode != MonitorTerminalExit {
		return "", false, fmt.Errorf("check exited with code %d", result.ExitCode)
	}
	if overflow && result.ExitCode != MonitorTerminalExit {
		return "", false, errors.New("check output exceeds 16 KiB")
	}
	if overflow {
		return "", true, nil
	}
	return hex.EncodeToString(hash.Sum(nil)), result.ExitCode == MonitorTerminalExit, nil
}
