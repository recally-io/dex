package execenv

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	envnode "github.com/MichaelKinsy/PiG/durable/env/node"
)

type localProvider struct {
	dataDir string
	home    string
}

// NewLocalProvider returns an execution environment provider rooted on the control host under dataDir.
// An empty home falls back to os.UserHomeDir when resolving Home.
func NewLocalProvider(dataDir, home string) Provider {
	cleanHome := ""
	if home != "" {
		cleanHome = filepath.Clean(home)
	}
	return &localProvider{dataDir: filepath.Clean(dataDir), home: cleanHome}
}

func (p *localProvider) Open(ctx context.Context, agentID string) (Handle, error) {
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
	return &localHandle{id: agentID, workspace: workspace, home: p.home}, nil
}

func (p *localProvider) Close(context.Context) error { return nil }

type localHandle struct {
	id        string
	workspace string
	home      string
}

func (h *localHandle) Kind() Kind        { return KindLocal }
func (h *localHandle) ID() string        { return h.id }
func (h *localHandle) Workspace() string { return h.workspace }

func (h *localHandle) Home(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if h.home != "" {
		return h.home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Clean(home), nil
}

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
