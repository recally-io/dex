package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"

	"github.com/recally-io/dex/internal/execenv"
)

// sessionEnv is the execution environment of one session: a lease and the tool environment
// values for its kind.
type sessionEnv struct {
	// ID is the explicit selection of the session, or empty when it follows the agent.
	ID     string
	Handle execenv.Handle
	// Vars are the tool environment values. A remote environment gets only the explicit .env
	// values, and its processes inherit the environment of their host (Inherit).
	Vars    map[string]string
	Inherit bool
}

// environments holds the leases of one open agent: its own environment and the explicit
// environments that its sessions select. Opens run under mu, outside the engine state lock.
type environments struct {
	agentID  string
	provider execenv.Provider
	// selected returns the explicit environment of a session, or empty when it follows the agent.
	selected func(ctx context.Context, ref Ref) (string, error)
	own      execenv.Handle
	loaded   *loadedExtensions

	mu     sync.Mutex
	leases map[string]execenv.Handle
}

func (e *environments) vars(h execenv.Handle) (map[string]string, bool) {
	if e.loaded == nil {
		return nil, h.Kind() != execenv.KindLocal
	}
	if h.Kind() == execenv.KindLocal {
		return e.loaded.Env, false
	}
	return e.loaded.ExplicitEnv, true
}

// session returns the environment of the unscoped session conversationID. A session that selects
// an environment fails with execenv.ErrUnavailable when that environment was removed or is not
// allowed: it never runs in the environment of the agent instead.
func (e *environments) session(ctx context.Context, conversationID int64) (sessionEnv, error) {
	id := ""
	if e.selected != nil && conversationID > 0 {
		var err error
		if id, err = e.selected(ctx, Ref{AgentID: e.agentID, ID: conversationID}); err != nil {
			return sessionEnv{}, err
		}
	}
	return e.byID(ctx, id)
}

// byID returns the lease of an explicit environment, or the agent's own environment for an empty id.
func (e *environments) byID(ctx context.Context, id string) (sessionEnv, error) {
	if id == "" {
		vars, inherit := e.vars(e.own)
		return sessionEnv{Handle: e.own, Vars: vars, Inherit: inherit}, nil
	}
	// The stamp check reads control storage at each call, so a removed environment
	// stops the next tool call. A changed stamp makes the agent stale; reload replaces the lease.
	if _, err := e.provider.Stamp(ctx, e.agentID, id); err != nil {
		return sessionEnv{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	h := e.leases[id]
	if h == nil {
		var err error
		if h, err = e.provider.Open(ctx, e.agentID, id); err != nil {
			return sessionEnv{}, err
		}
		if e.leases == nil {
			e.leases = map[string]execenv.Handle{}
		}
		e.leases[id] = h
	}
	vars, inherit := e.vars(h)
	return sessionEnv{ID: id, Handle: h, Vars: vars, Inherit: inherit}, nil
}

// stale reports whether the configuration of an open explicit lease changed or became unavailable.
func (e *environments) stale(ctx context.Context) bool {
	e.mu.Lock()
	leases := maps.Clone(e.leases)
	e.mu.Unlock()
	for id, h := range leases {
		if current, err := e.provider.Stamp(ctx, e.agentID, id); err != nil || current != h.Stamp() {
			return true
		}
	}
	return false
}

func (e *environments) close(ctx context.Context) error {
	e.mu.Lock()
	leases := e.leases
	e.leases = nil
	e.mu.Unlock()
	var errs []error
	for _, h := range leases {
		errs = append(errs, h.Close(ctx))
	}
	return errors.Join(errs...)
}

// withEnvironment runs fn with the lease of environmentID for agentID: the open agent's lease, or
// a temporary lease when the agent is not open. An empty environmentID selects the agent's own.
func (a *Agents) withEnvironment(ctx context.Context, agentID, environmentID string, fn func(execenv.Handle) error) error {
	if !agentIDPattern.MatchString(agentID) {
		return ErrInvalidInput
	}
	if err := a.begin(); err != nil {
		return err
	}
	defer a.end()
	if envs := a.environments(agentID); envs != nil {
		s, err := envs.byID(ctx, environmentID)
		if err != nil {
			return err
		}
		return fn(s.Handle)
	}
	h, err := a.config.ExecEnv.Open(ctx, agentID, environmentID)
	if err != nil {
		return err
	}
	defer func() { _ = h.Close(context.WithoutCancel(ctx)) }()
	return fn(h)
}

func (a *Agents) environments(agentID string) *environments {
	a.mu.Lock()
	defer a.mu.Unlock()
	if res := a.resources[agentID]; res != nil {
		return res.envs
	}
	return nil
}

// sessionEnvironmentID returns the explicit environment of a session, or empty when it follows its agent.
func (a *Agents) sessionEnvironmentID(ctx context.Context, ref Ref) (string, error) {
	if ref.Scope != "" || ref.ID <= 0 || a.config.SessionEnvironment == nil {
		return "", nil
	}
	id, err := a.config.SessionEnvironment(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("read the environment of the session: %w", err)
	}
	return id, nil
}
