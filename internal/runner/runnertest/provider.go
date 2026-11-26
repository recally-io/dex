// Package runnertest provides isolated execution environment test providers with no production footprint.
package runnertest

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	envnode "github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/recally-io/dex/internal/runner"
)

// NewProvider returns a Provider of kind rooted at rootDir/agents/<id>/workspace with homeDir and isolated baseEnv.
func NewProvider(kind runner.Kind, rootDir, homeDir string, baseEnv map[string]string) runner.Provider {
	return providerFunc(func(ctx context.Context, agentID, runnerID string) (runner.Handle, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if runnerID != "" {
			return nil, fmt.Errorf("%w: the test provider has only the bot environment", runner.ErrUnavailable)
		}
		ws := filepath.Join(rootDir, "agents", agentID, "workspace")
		if err := os.MkdirAll(ws, 0o700); err != nil {
			return nil, err
		}
		envVars := map[string]string{"PATH": os.Getenv("PATH"), "HOME": homeDir}
		maps.Copy(envVars, baseEnv)
		return &isolatedHandle{kind: kind, ws: ws, home: homeDir, baseEnv: envVars}, nil
	})
}

type providerFunc func(ctx context.Context, agentID, runnerID string) (runner.Handle, error)

func (f providerFunc) Open(ctx context.Context, agentID, runnerID string) (runner.Handle, error) {
	return f(ctx, agentID, runnerID)
}

func (f providerFunc) Stamp(context.Context, string, string) (string, error) { return "", nil }
func (f providerFunc) Close(context.Context) error                           { return nil }

type isolatedHandle struct {
	kind    runner.Kind
	ws      string
	home    string
	baseEnv map[string]string
}

func (h *isolatedHandle) Kind() runner.Kind           { return h.kind }
func (h *isolatedHandle) Workspace() string           { return h.ws }
func (h *isolatedHandle) Home() string                { return h.home }
func (h *isolatedHandle) Stamp() string               { return "" }
func (h *isolatedHandle) Close(context.Context) error { return nil }
func (h *isolatedHandle) Env(ctx context.Context, cwd string, extraEnv map[string]string) (durableenv.ExecutionEnv, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	merged := maps.Clone(h.baseEnv)
	maps.Copy(merged, extraEnv)
	base := envnode.NewNodeExecutionEnv(envnode.NodeExecutionEnvOptions{Cwd: cmp.Or(cwd, h.ws)})
	return &isolatedExecEnv{ExecutionEnv: base, env: merged}, nil
}

type isolatedExecEnv struct {
	durableenv.ExecutionEnv
	env map[string]string
}

func (e *isolatedExecEnv) Exec(ctx context.Context, command any, opts *durableenv.ShellExecOptions) (durableenv.ShellExecResult, error) {
	merged := maps.Clone(e.env)
	var next durableenv.ShellExecOptions
	if opts != nil {
		next = *opts
		maps.Copy(merged, opts.Env)
	}
	next.Env = merged
	next.InheritEnv = new(false)
	return e.ExecutionEnv.Exec(ctx, command, &next)
}
