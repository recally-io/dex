package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	"github.com/recally-io/dex/internal/runner"
	"github.com/recally-io/dex/internal/runner/outbound"
)

// sessionEnv is the execution environment of one session: a lease and the tool environment
// values for its kind.
type sessionEnv struct {
	// ID is the explicit selection of the session, or empty when it follows the agent.
	ID     string
	Handle runner.Handle
	Files  *agentExtensions
	// Vars hold the base values, without any vault secrets. Remote commands use the target's
	// environment with only the configured 1Password credentials from the control host.
	Vars    map[string]string
	Inherit bool
}

func (s sessionEnv) executionEnv(ctx context.Context, cwd string) (durableenv.ExecutionEnv, error) {
	if cwd == "" {
		cwd = s.Handle.Workspace()
	}
	env, err := s.Handle.Env(ctx, cwd, s.Vars)
	if err == nil && s.Files != nil {
		s.Files.trySyncSkills(ctx, env, false)
	}
	return env, err
}

// environments owns the leases shared by sessions that select the same environment.
// Only opens of the same lease wait for each other.
type environments struct {
	agentID  string
	provider runner.Provider
	// selected returns the explicit environment of a session, or empty when it follows the agent.
	selected func(ctx context.Context, ref Ref) (string, error)
	files    *agentExtensions
	loaded   *loadedExtensions
	// Restore a default that was offline at cold open or failed to open on reload.
	restoreDefault bool

	mu         sync.Mutex
	own        runner.Handle
	ownFiles   *agentExtensions
	ownErr     error
	retryAfter time.Time
	retryStamp string
	leases     map[string]sessionEnv
	opening    map[string]chan struct{}
	closed     bool
}

func (e *environments) vars(h runner.Handle) (map[string]string, bool) {
	if e.loaded == nil {
		return nil, h.Kind() != runner.KindLocal
	}
	if h.Kind() == runner.KindLocal {
		return e.loaded.Env, false
	}
	return e.loaded.ExplicitEnv, true
}

// session returns the environment of the session conversationID. A session that selects
// an environment fails with runner.ErrUnavailable when that environment was removed or is not
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
	if id != "" && e.defaultUnopened() {
		// An explicit selection of the default shares its lease even on first open.
		// In particular, do not start two opens of the same persisted E2B sandbox.
		if current, err := e.provider.Stamp(ctx, e.agentID, ""); err == nil && current == stamp {
			s, err := e.byID(ctx, "")
			if err == nil && s.Handle.Stamp() != stamp {
				return sessionEnv{}, fmt.Errorf("%w: the selected environment changed; retry", runner.ErrUnavailable)
			}
			s.ID = id
			return s, err
		}
	}
	for {
		e.mu.Lock()
		if e.closed {
			e.mu.Unlock()
			return sessionEnv{}, ErrClosed
		}
		own, ownFiles := e.own, e.ownFiles
		if own != nil && stamp == own.Stamp() {
			e.mu.Unlock()
			useOwn := true
			if id != "" && own.Kind() == runner.KindE2B {
				// A pinned selection cannot use the bot's old sandbox after a default change.
				current, err := e.provider.Stamp(ctx, e.agentID, "")
				useOwn = err == nil && current == stamp
			}
			if useOwn {
				vars, inherit := e.vars(own)
				return sessionEnv{ID: id, Handle: own, Files: ownFiles, Vars: vars, Inherit: inherit}, nil
			}
			e.mu.Lock()
		}
		if id == "" && own != nil {
			e.mu.Unlock()
			return sessionEnv{}, fmt.Errorf("%w: the bot environment changed; wait for reload", runner.ErrUnavailable)
		}
		if active := e.opening[id]; active != nil {
			e.mu.Unlock()
			select {
			case <-ctx.Done():
				return sessionEnv{}, ctx.Err()
			case <-active:
				stamp, err = e.provider.Stamp(ctx, e.agentID, id)
				if err != nil {
					return sessionEnv{}, err
				}
				continue
			}
		}
		previous := e.leases[id]
		if h := previous.Handle; h != nil {
			if stamp != h.Stamp() {
				e.mu.Unlock()
				return sessionEnv{}, fmt.Errorf("%w: the selected environment changed; wait for reload", runner.ErrUnavailable)
			}
			if !runner.LeaseExpired(h) {
				e.mu.Unlock()
				return previous, nil
			}
			delete(e.leases, id)
		}
		done := make(chan struct{})
		if e.opening == nil {
			e.opening = map[string]chan struct{}{}
		}
		e.opening[id] = done
		var recentErr error
		if id == "" && stamp == e.retryStamp && time.Now().Before(e.retryAfter) {
			recentErr = e.ownErr
		}
		e.mu.Unlock()
		return e.openLease(ctx, id, stamp, previous.Handle, recentErr, done)
	}
}

func (e *environments) defaultUnopened() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.own == nil
}

func (e *environments) openLease(ctx context.Context, id, stamp string, expired runner.Handle, recentErr error, done chan struct{}) (sessionEnv, error) {
	defer func() {
		e.mu.Lock()
		delete(e.opening, id)
		close(done)
		e.mu.Unlock()
	}()
	if recentErr != nil && !e.backOnline(ctx, recentErr) {
		return sessionEnv{}, recentErr
	}
	if expired != nil {
		if err := expired.Close(ctx); err != nil {
			return sessionEnv{}, err
		}
	}
	h, err := e.provider.Open(ctx, e.agentID, id)
	if err != nil {
		if id == "" && ctx.Err() == nil {
			e.mu.Lock()
			e.ownErr, e.retryStamp, e.retryAfter = err, stamp, time.Now().Add(time.Second)
			e.mu.Unlock()
		}
		return sessionEnv{}, err
	}
	vars, inherit := e.vars(h)
	s := sessionEnv{ID: id, Handle: h, Files: e.files.forHandle(h), Vars: vars, Inherit: inherit}
	s.Files.trySyncSkills(ctx, nil, true)
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return sessionEnv{}, errors.Join(ErrClosed, h.Close(context.WithoutCancel(ctx)))
	}
	if id == "" {
		e.own, e.ownFiles, e.ownErr = h, s.Files, nil
	} else {
		if e.leases == nil {
			e.leases = map[string]sessionEnv{}
		}
		e.leases[id] = s
	}
	e.mu.Unlock()
	return s, nil
}

// defaultFiles returns the current inspection target and whether the default lease
// opened after an unavailable default was recorded.
func (e *environments) defaultFiles() (*agentExtensions, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ownFiles, e.own != nil && e.restoreDefault
}

// restorable reports whether a reload can restore an unavailable default: a later
// call opened it, or its machine was offline and is connected now. A reload opens the default
// itself, so the check does not open a lease. Other kinds wait for a call that opens them,
// because a failing SSH or sandbox endpoint must not rebuild the agent at each check.
func (e *environments) restorable(ctx context.Context) bool {
	if !e.restoreDefault {
		return false
	}
	e.mu.Lock()
	own, ownErr := e.own, e.ownErr
	e.mu.Unlock()
	return own != nil || e.backOnline(ctx, ownErr)
}

// backOnline reports that err is the offline error of a machine that is connected now.
func (e *environments) backOnline(ctx context.Context, err error) bool {
	return errors.Is(err, outbound.ErrOffline) && e.status(ctx, "") == "available"
}

// status is the provider report for an environment of the agent: "available", "offline", or
// the reason why it is unavailable. A provider without reports is always available.
func (e *environments) status(ctx context.Context, id string) string {
	if p, ok := e.provider.(interface {
		Status(ctx context.Context, agentID, runnerID string) string
	}); ok {
		return p.Status(ctx, e.agentID, id)
	}
	return "available"
}

func (e *environments) hasLease(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.leases[id]
	return ok
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

// withRunner runs fn with the lease of runnerID for agentID: the open agent's lease, or
// a temporary lease for the default when the agent is not open. Explicit browsing opens the
// agent (and loads its configured MCP servers) so subsequent operations use the
// same lease, including disposable sandboxes.
func (a *Agents) withRunner(ctx context.Context, agentID, runnerID string, fn func(runner.Handle) error) error {
	if !agentIDPattern.MatchString(agentID) {
		return ErrInvalidInput
	}
	if err := a.begin(agentID); err != nil {
		return err
	}
	defer a.end(agentID)
	if a.environments(agentID) == nil && runnerID != "" {
		if _, err := a.config.ExecEnv.Stamp(ctx, agentID, runnerID); err != nil {
			return err
		}
		if _, err := a.harness(ctx, agentID); err != nil {
			return err
		}
	}
	if envs := a.environments(agentID); envs != nil {
		s, err := envs.byID(ctx, runnerID)
		if err != nil {
			return err
		}
		return fn(s.Handle)
	}
	h, err := a.config.ExecEnv.Open(ctx, agentID, runnerID)
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

// sessionRunnerID returns the explicit environment of a session, or empty when it follows its agent.
func (a *Agents) sessionRunnerID(ctx context.Context, ref Ref) (string, error) {
	if ref.ID <= 0 || a.config.SessionRunner == nil {
		return "", nil
	}
	id, err := a.config.SessionRunner(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("read the environment of the session: %w", err)
	}
	return id, nil
}
