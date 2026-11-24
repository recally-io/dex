package execenv

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/execenv/runner"
)

var ErrRunnerCredential = errors.New("the machine certificate is not valid")
var ErrRunnerConflict = errors.New("this environment already has a machine; revoke it before connecting another machine")

// runnerGrace is how long a call waits for its machine to connect again before it fails.
// The machine agent dials again one second after a lost session.
const runnerGrace = 10 * time.Second

// Runners owns the enrollment and the live sessions of remote machines. The HTTP routes
// call it directly; the machine backend reads its sessions.
//
// A session is a connection, not an identity. The identity of a machine is its enrollment, the
// credential digest in control storage. A reconnect with the same token keeps the identity, so
// the leases of loaded bots stay valid across it.
type Runners struct {
	q        *sqlc.Queries
	mu       sync.Mutex
	sessions map[string]liveRunner
	// changed closes when a session starts or ends, or a machine is revoked, to wake the calls
	// that wait for a session.
	changed  chan struct{}
	grace    time.Duration
	onOnline func(id string)
	closed   bool
}

type liveRunner struct {
	client *runner.Client
	digest string
}

// errRunnerOffline is the error of a call that found no session. It never reached the machine.
func errRunnerOffline() error { return fmt.Errorf("%w: %w", ErrUnavailable, runner.ErrOffline) }

func newRunners(q *sqlc.Queries) *Runners {
	return &Runners{q: q, sessions: map[string]liveRunner{}, changed: make(chan struct{}), grace: runnerGrace}
}

func runnerDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// IssueCode creates a ten-minute enrollment code for a saved remote machine.
func (rt *Runners) IssueCode(ctx context.Context, id string) (string, error) {
	code := rand.Text()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	n, err := rt.q.IssueRunnerCode(ctx, sqlc.IssueRunnerCodeParams{EnvironmentID: id, CodeDigest: runnerDigest(code), ExpiresAt: time.Now().Add(10 * time.Minute).UnixMilli()})
	if err == nil && n != 1 {
		err = ErrRunnerConflict
	}
	if err != nil {
		return "", err
	}
	return code, nil
}

// Enroll consumes a one-time code and returns the token of the machine. Dex stores only
// the SHA-256 digest of the token.
func (rt *Runners) Enroll(ctx context.Context, id, code string) (string, error) {
	token := rand.Text()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	n, err := rt.q.RedeemRunnerCode(ctx, sqlc.RedeemRunnerCodeParams{EnvironmentID: id, CodeDigest: runnerDigest(code), CredentialDigest: runnerDigest(token), Now: time.Now().UnixMilli()})
	if err == nil && n != 1 {
		err = ErrRunnerCredential
	}
	if err != nil {
		return "", err
	}
	return token, nil
}

// Verify checks the machine token before the HTTP upgrade.
func (rt *Runners) Verify(ctx context.Context, id, token string) error {
	digest, err := rt.q.GetRunnerCredential(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrRunnerCredential
	}
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(digest), []byte(runnerDigest(token))) != 1 {
		return ErrRunnerCredential
	}
	return nil
}

// OnOnline sets fn to run after each machine session starts. Set it before the HTTP server starts.
func (rt *Runners) OnOnline(fn func(id string)) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.onOnline = fn
}

// notifyLocked wakes the calls that wait for a session. The caller holds mu.
func (rt *Runners) notifyLocked() {
	close(rt.changed)
	rt.changed = make(chan struct{})
}

// Accept installs a ready connection after checking that its token is still valid.
// A new session with the valid token replaces the earlier session of the same machine: the
// earlier socket can be half open for up to two heartbeat periods after the machine left it.
// It returns when the session ends.
func (rt *Runners) Accept(ctx context.Context, id, token string, socket *websocket.Conn) error {
	session, err := runner.Connect(ctx, socket, id)
	if err != nil {
		return err
	}
	defer session.Close()
	rt.mu.Lock()
	err = rt.Verify(ctx, id, token)
	if err == nil && rt.closed {
		err = runner.ErrOffline
	}
	var earlier *runner.Client
	if err == nil {
		earlier = rt.sessions[id].client
		rt.sessions[id] = liveRunner{client: session, digest: runnerDigest(token)}
		rt.notifyLocked()
	}
	onOnline := rt.onOnline
	rt.mu.Unlock()
	if err != nil {
		return err
	}
	if earlier != nil {
		// The calls that the earlier session dispatched report an unknown result. Two machines
		// that use one token also get here, so make it visible.
		slog.Warn("machine connected again; closing its earlier session", "environment", id)
		earlier.Close()
	}
	defer func() {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if rt.sessions[id].client == session {
			delete(rt.sessions, id)
			rt.notifyLocked()
		}
	}()
	if onOnline != nil {
		onOnline(id)
	}
	select {
	case <-session.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Revoke removes the enrollment and disconnects the machine.
func (rt *Runners) Revoke(ctx context.Context, id string) error {
	rt.mu.Lock()
	err := rt.q.RevokeRunnerCredential(ctx, id)
	var session *runner.Client
	if err == nil {
		session = rt.sessions[id].client
		delete(rt.sessions, id)
		rt.notifyLocked()
	}
	rt.mu.Unlock()
	if session != nil {
		session.Close()
	}
	return err
}

// RunnerStatus describes the live connection state and version metadata of one remote machine.
type RunnerStatus struct {
	Online           bool
	OS               string
	Arch             string
	Version          string
	CanSelfUpdate    bool
	SelfUpdateReason string
}

// Status reports whether id is currently online and the metadata reported by its live session.
func (rt *Runners) Status(id string) RunnerStatus {
	rt.mu.Lock()
	session := rt.sessions[id].client
	rt.mu.Unlock()
	if session == nil {
		return RunnerStatus{}
	}
	info := session.Info()
	return RunnerStatus{
		Online:           true,
		OS:               info.OS,
		Arch:             info.Arch,
		Version:          info.Version,
		CanSelfUpdate:    info.CanSelfUpdate,
		SelfUpdateReason: info.SelfUpdateReason,
	}
}

func (rt *Runners) online(id string) *runner.Client {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.sessions[id].client
}

// identity is the credential digest of the enrollment of id, or empty when no machine is
// enrolled. A revoke or a new enrollment changes it. A reconnect or a restart of Dex does not.
func (rt *Runners) identity(ctx context.Context, id string) (string, error) {
	digest, err := rt.q.GetRunnerCredential(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return digest, err
}

// sessionLocked returns the live session of id when it uses the enrollment digest. The caller
// holds mu.
func (rt *Runners) sessionLocked(id, digest string) (*runner.Client, error) {
	s := rt.sessions[id]
	switch {
	case rt.closed || s.client == nil:
		return nil, errRunnerOffline()
	case s.digest != digest:
		return nil, fmt.Errorf("%w: the machine enrollment changed; wait for reload", ErrUnavailable)
	}
	return s.client, nil
}

// wait returns the live session like sessionLocked, but waits up to the grace period for an
// offline machine to connect again. It checks the enrollment at each change, so a revoke or a
// new enrollment fails at once. The wait ends early when ctx ends.
func (rt *Runners) wait(ctx context.Context, id, digest string) (*runner.Client, error) {
	var expired <-chan time.Time
	for {
		rt.mu.Lock()
		client, err := rt.sessionLocked(id, digest)
		changed, closed := rt.changed, rt.closed
		rt.mu.Unlock()
		if err == nil || closed || !errors.Is(err, runner.ErrOffline) {
			return client, err
		}
		if current, err := rt.identity(ctx, id); err != nil {
			return nil, err
		} else if current != digest {
			return nil, fmt.Errorf("%w: the machine enrollment changed; wait for reload", ErrUnavailable)
		}
		if expired == nil {
			timer := time.NewTimer(rt.grace)
			defer timer.Stop()
			expired = timer.C
		}
		select {
		case <-changed:
		case <-expired:
			return nil, err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (rt *Runners) close() {
	rt.mu.Lock()
	rt.closed = true
	sessions := rt.sessions
	rt.sessions = nil
	rt.notifyLocked()
	rt.mu.Unlock()
	for _, session := range sessions {
		session.client.Close()
	}
}
