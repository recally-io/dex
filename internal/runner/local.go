package runner

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	envnode "github.com/MichaelKinsy/PiG/durable/env/node"
)

const localStamp = string(KindLocal)

type localProvider struct {
	dataDir string
	home    string
}

// NewLocalProvider returns an execution environment provider rooted on the control host under dataDir.
// An empty home selects os.UserHomeDir at Open.
func NewLocalProvider(dataDir, home string) Provider {
	return newLocalProvider(dataDir, home)
}

func newLocalProvider(dataDir, home string) *localProvider {
	cleanHome := ""
	if home != "" {
		cleanHome = filepath.Clean(home)
	}
	return &localProvider{dataDir: filepath.Clean(dataDir), home: cleanHome}
}

func (p *localProvider) Open(ctx context.Context, agentID, runnerID string) (Handle, error) {
	if err := localOnly(runnerID); err != nil {
		return nil, err
	}
	l, err := p.open(ctx, agentID)
	if err != nil {
		return nil, err
	}
	return stampedHandle{lease: l, stamp: localStamp}, nil
}

func (p *localProvider) open(ctx context.Context, agentID string) (lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateAgentID(agentID); err != nil {
		return nil, err
	}
	workspace := filepath.Join(p.dataDir, "agents", agentID, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return nil, err
	}
	home := p.home
	if home == "" {
		// A host without a home directory leaves home empty; callers that need it report ErrInvalidPath.
		if dir, err := os.UserHomeDir(); err == nil {
			home = filepath.Clean(dir)
		}
	}
	return &localHandle{workspace: workspace, home: home}, nil
}

func (p *localProvider) Stamp(_ context.Context, _, runnerID string) (string, error) {
	if err := localOnly(runnerID); err != nil {
		return "", err
	}
	return localStamp, nil
}

// localOnly accepts the selections that the provider without control storage can serve.
func localOnly(runnerID string) error {
	if runnerID != "" && runnerID != LocalID {
		return fmt.Errorf("%w: %s is not configured", ErrUnavailable, runnerID)
	}
	return nil
}

func (p *localProvider) Close(context.Context) error { return nil }

type localHandle struct {
	workspace string
	home      string
}

func (h *localHandle) Kind() Kind        { return KindLocal }
func (h *localHandle) Workspace() string { return h.workspace }

func (h *localHandle) Home() string { return h.home }

func (h *localHandle) Env(ctx context.Context, cwd string, extraEnv map[string]string) (durableenv.ExecutionEnv, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return envnode.NewNodeExecutionEnv(envnode.NodeExecutionEnvOptions{
		Cwd:      cmp.Or(cwd, h.workspace),
		ShellEnv: maps.Clone(extraEnv),
	}), nil
}

func (h *localHandle) Close(context.Context) error { return nil }

func validateAgentID(agentID string) error {
	if agentID == "" || agentID == "." || agentID == ".." || strings.ContainsAny(agentID, `/\`) {
		return errors.New("invalid agent ID")
	}
	return nil
}
