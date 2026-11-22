package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

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
	files    *agentExtensions
	loaded   *loadedExtensions

	mu         sync.Mutex
	own        execenv.Handle
	ownFiles   *agentExtensions
	ownErr     error
	retryAfter time.Time
	retryStamp string
	leases     map[string]sessionEnv
	closed     bool
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
	s, err := e.byID(ctx, id)
	s.ID = id
	return s, err
}

// byID returns the lease of an explicit environment, or the agent's own environment for an empty id.
func (e *environments) byID(ctx context.Context, id string) (sessionEnv, error) {
	// Read control storage at every call. Never execute with a removed or stale lease.
	stamp, err := e.provider.Stamp(ctx, e.agentID, id)
	if err != nil {
		return sessionEnv{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return sessionEnv{}, ErrClosed
	}
	if id == "" && e.own == nil {
		// Admission and tool resolution can both ask for the default in one turn.
		// Back off on repeated failures, but let a changed binding retry immediately.
		if stamp == e.retryStamp && time.Now().Before(e.retryAfter) {
			return sessionEnv{}, e.ownErr
		}
		h, err := e.provider.Open(ctx, e.agentID, "")
		if err != nil {
			if ctx.Err() == nil {
				e.ownErr, e.retryStamp, e.retryAfter = err, stamp, time.Now().Add(time.Second)
			}
			return sessionEnv{}, err
		}
		e.own, e.ownFiles, e.ownErr = h, e.files.forHandle(h), nil
		stamp = h.Stamp()
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
		return sessionEnv{ID: id, Handle: e.own, Files: e.ownFiles, Vars: vars, Inherit: inherit}, nil
	}
	if id == "" {
		return sessionEnv{}, fmt.Errorf("%w: the bot environment changed; wait for reload", execenv.ErrUnavailable)
	}
	if s, ok := e.leases[id]; ok {
		if stamp != s.Handle.Stamp() {
			return sessionEnv{}, fmt.Errorf("%w: the selected environment changed; wait for reload", execenv.ErrUnavailable)
		}
		if !execenv.LeaseExpired(s.Handle) {
			return s, nil
		}
		// Expiry is specific to this lease, not the configuration or other bots.
		delete(e.leases, id)
		if err := s.Handle.Close(ctx); err != nil {
			return sessionEnv{}, err
		}
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

// defaultFiles returns the current default inspection target and whether an idle
// reload must restore MCP tools that could not load while the default was down.
func (e *environments) defaultFiles() (*agentExtensions, bool) {
	e.mu.Lock()
	files, recovered := e.ownFiles, e.own != nil && e.files.defaultUnavailable
	e.mu.Unlock()
	if recovered {
		for _, server := range e.loaded.serverInfo() {
			if server.State == "failed" {
				return files, true
			}
		}
	}
	return files, false
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
	e.closed = true
	own, leases := e.own, e.leases
	e.own, e.leases = nil, nil
	e.mu.Unlock()
	var errs []error
	if own != nil {
		errs = append(errs, own.Close(ctx))
	}
	for _, h := range leases {
		errs = append(errs, h.Handle.Close(ctx))
	}
	return errors.Join(errs...)
}

// withEnvironment runs fn with the lease of environmentID for agentID: the open agent's lease, or
// a temporary lease for the default when the agent is not open. Explicit browsing opens the
// agent (and loads its configured MCP servers) so subsequent operations use the
// same lease, including disposable sandboxes.
func (a *Agents) withEnvironment(ctx context.Context, agentID, environmentID string, fn func(execenv.Handle) error) error {
	if !agentIDPattern.MatchString(agentID) {
		return ErrInvalidInput
	}
	if err := a.begin(); err != nil {
		return err
	}
	defer a.end()
	if a.environments(agentID) == nil && environmentID != "" {
		if _, err := a.config.ExecEnv.Stamp(ctx, agentID, environmentID); err != nil {
			return err
		}
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
