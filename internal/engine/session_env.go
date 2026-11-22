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
	Files  *agentExtensions
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
	ownErr   error
	files    *agentExtensions
	loaded   *loadedExtensions

	mu     sync.Mutex
	leases map[string]sessionEnv
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
	if id == "" && e.ownErr != nil {
		return sessionEnv{}, e.ownErr
	}
	// Read control storage at every call. Never execute with a removed or stale lease.
	stamp, err := e.provider.Stamp(ctx, e.agentID, id)
	if err != nil {
		return sessionEnv{}, err
	}
	useOwn := e.own != nil && stamp == e.own.Stamp()
	if useOwn && id != "" && e.own.Kind() == execenv.KindE2B {
		// A pinned E2B selection can remain after the bot changes its default. In that
		// case, it must not use the bot's old persisted sandbox lease.
		current, err := e.provider.Stamp(ctx, e.agentID, "")
		useOwn = err == nil && current == stamp
	}
	if useOwn {
		vars, inherit := e.vars(e.own)
		return sessionEnv{ID: id, Handle: e.own, Files: e.files, Vars: vars, Inherit: inherit}, nil
	}
	if id == "" {
		return sessionEnv{}, fmt.Errorf("%w: the bot environment changed; wait for reload", execenv.ErrUnavailable)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if s, ok := e.leases[id]; ok {
		if stamp != s.Handle.Stamp() {
			return sessionEnv{}, fmt.Errorf("%w: the selected environment changed; wait for reload", execenv.ErrUnavailable)
		}
		return s, nil
	}
	h, err := e.provider.Open(ctx, e.agentID, id)
	if err != nil {
		return sessionEnv{}, err
	}
	if e.leases == nil {
		e.leases = map[string]sessionEnv{}
	}
	vars, inherit := e.vars(h)
	s := sessionEnv{ID: id, Handle: h, Files: e.files.forHandle(h), Vars: vars, Inherit: inherit}
	e.leases[id] = s
	return s, nil
}

// stale reports whether the configuration of an open explicit lease changed or became unavailable.
func (e *environments) stale(ctx context.Context) bool {
	e.mu.Lock()
	leases := maps.Clone(e.leases)
	e.mu.Unlock()
	for id, h := range leases {
		if current, err := e.provider.Stamp(ctx, e.agentID, id); err != nil || current != h.Handle.Stamp() {
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
		errs = append(errs, h.Handle.Close(ctx))
	}
	return errors.Join(errs...)
}

// withEnvironment runs fn with the lease of environmentID for agentID: the open agent's lease, or
// a temporary lease for the default when the agent is not open. Explicit browsing opens the
// agent so subsequent operations see the same lease, including disposable sandboxes.
func (a *Agents) withEnvironment(ctx context.Context, agentID, environmentID string, fn func(execenv.Handle) error) error {
	if !agentIDPattern.MatchString(agentID) {
		return ErrInvalidInput
	}
	if err := a.begin(); err != nil {
		return err
	}
	defer a.end()
	if a.environments(agentID) == nil && environmentID != "" {
		if _, err := a.harness(ctx, agentID, ""); err != nil {
			return err
		}
	}
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
