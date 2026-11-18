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
)

// ErrInvalidPath is returned when a requested project or directory path fails validation.
var ErrInvalidPath = errors.New("invalid project directory")

// ErrInvalidConfig is returned when an execution environment configuration is invalid.
var ErrInvalidConfig = errors.New("invalid execution environment configuration")

// Handle represents an opened execution environment bound to one agent.
type Handle interface {
	Kind() Kind
	Workspace() string
	Home(ctx context.Context) (string, error)
	Env(ctx context.Context, cwd string, extraEnv map[string]string) (durableenv.ExecutionEnv, error)
	Close(ctx context.Context) error
}

// Provider opens and manages agent execution environment handles.
type Provider interface {
	Open(ctx context.Context, agentID string) (Handle, error)
	Stamp(ctx context.Context, agentID string) (string, bool)
	Close(ctx context.Context) error
}
