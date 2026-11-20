// Package execenv decouples agent workspace, filesystem, and shell execution from the control host.
package execenv

import (
	"context"
	"errors"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
)

// Kind identifies an agent execution environment backend.
type Kind string

const (
	// KindLocal runs tools and shell commands directly on the control host.
	KindLocal Kind = "local"
	// KindPiEnv runs tools and shell commands over a PiG pipe daemon connection.
	KindPiEnv Kind = "pi-env"
	// KindE2B runs tools and shell commands inside an E2B sandbox.
	KindE2B Kind = "e2b"
	// KindRunner runs tools on a host that connects to Dex.
	KindRunner Kind = "runner"
)

// ErrInvalidPath is returned when a requested project or directory path fails validation.
var ErrInvalidPath = errors.New("invalid project directory")

// ErrInvalidConfig is returned when an execution environment configuration is invalid.
var ErrInvalidConfig = errors.New("invalid execution environment configuration")

// ErrUnavailable is returned when a selected environment was removed or is not allowed for the
// agent. The caller must not run the work in another environment.
var ErrUnavailable = errors.New("execution environment is unavailable")

// LocalID selects the control host. The control host has no execution_environments row.
const LocalID = "local"

// Handle is one agent's lease on an opened execution environment.
// The backend owns the connection; Close releases only the lease.
type Handle interface {
	Kind() Kind
	// Stamp is the fingerprint of the configuration that Open read.
	// It differs from Provider.Stamp when the handle is stale.
	Stamp() string
	Workspace() string
	// Home is the home directory of the environment, or empty when the host has none.
	Home() string
	Env(ctx context.Context, cwd string, extraEnv map[string]string) (durableenv.ExecutionEnv, error)
	Close(ctx context.Context) error
}

// Provider opens agent execution environment handles. An empty environmentID selects the
// environment of the agent. LocalID or an environment ID selects that environment, which must be
// the environment of the agent or on its allowlist; otherwise Open and Stamp fail with
// ErrUnavailable.
type Provider interface {
	Open(ctx context.Context, agentID, environmentID string) (Handle, error)
	// Stamp returns the current fingerprint of the selected environment.
	Stamp(ctx context.Context, agentID, environmentID string) (string, error)
	Close(ctx context.Context) error
}

// lease is a Handle without its stamp. A backend returns it, and the Manager adds the stamp
// from the same configuration read.
type lease interface {
	Kind() Kind
	Workspace() string
	Home() string
	Env(ctx context.Context, cwd string, extraEnv map[string]string) (durableenv.ExecutionEnv, error)
	Close(ctx context.Context) error
}

type stampedHandle struct {
	lease
	stamp string
}

func (h stampedHandle) Stamp() string { return h.stamp }

// backend is one remote environment kind. Each method gets the stored spec of one environment.
type backend interface {
	// normalize validates spec and returns its normalized JSON config.
	normalize(spec EnvironmentSpec) (string, error)
	// probe checks connectivity. saved is the stored spec when the user edits a saved environment.
	probe(ctx context.Context, saved *EnvironmentSpec, spec EnvironmentSpec) (TestResult, error)
	open(ctx context.Context, agentID string, spec EnvironmentSpec) (lease, error)
	// changed runs after the row changed. cur is nil when the row was deleted.
	changed(ctx context.Context, old EnvironmentSpec, cur *EnvironmentSpec) error
	// release drops the agent's state in the environment: an allowlist or environment change, an
	// archive, or a reset.
	// sandboxID is the agent's stored sandbox reference, or empty.
	release(ctx context.Context, agentID string, spec EnvironmentSpec, sandboxID string) error
	// disposable reports that release discards the agent's files, so the user can reset the environment.
	disposable() bool
	// generation identifies the live connection state of an environment beyond its stored spec.
	// A change makes the stamp of every earlier handle stale.
	generation(envID string) uint64
}
