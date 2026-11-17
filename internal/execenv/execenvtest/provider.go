// Package execenvtest provides isolated execution environment test providers with no production footprint.
package execenvtest

import (
	"cmp"
	"context"
	"maps"
	"os"
	"path/filepath"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	envnode "github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/recally-io/dex/internal/execenv"
)

// NewProvider returns a Provider of kind rooted at rootDir/agents/<id>/workspace with homeDir and isolated baseEnv.
func NewProvider(kind execenv.Kind, rootDir, homeDir string, baseEnv map[string]string) execenv.Provider {
	return execenv.ProviderFunc(func(ctx context.Context, agentID string) (execenv.Handle, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ws := filepath.Join(rootDir, "agents", agentID, "workspace")
		if err := os.MkdirAll(ws, 0o700); err != nil {
			return nil, err
		}
		envVars := map[string]string{"PATH": os.Getenv("PATH"), "HOME": homeDir}
		maps.Copy(envVars, baseEnv)
		return &isolatedHandle{kind: kind, id: agentID, ws: ws, home: homeDir, baseEnv: envVars}, nil
	})
}

type isolatedHandle struct {
	kind    execenv.Kind
	id      string
	ws      string
	home    string
	baseEnv map[string]string
}

func (h *isolatedHandle) Kind() execenv.Kind                       { return h.kind }
func (h *isolatedHandle) ID() string                               { return h.id }
func (h *isolatedHandle) Workspace() string                        { return h.ws }
func (h *isolatedHandle) Home(ctx context.Context) (string, error) { return h.home, ctx.Err() }
func (h *isolatedHandle) Close(context.Context) error              { return nil }
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
