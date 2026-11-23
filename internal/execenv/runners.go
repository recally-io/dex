package execenv

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/execenv/runner"
)

var ErrRunnerCredential = errors.New("the machine certificate is not valid")
var ErrRunnerConflict = errors.New("this environment already has a machine; revoke it before connecting another machine")

// Runners owns the enrollment and the live sessions of remote machines. The HTTP routes
// call it directly; the machine backend reads its sessions.
type Runners struct {
	q        *sqlc.Queries
	mu       sync.Mutex
	sessions map[string]liveRunner
	// lastGeneration numbers sessions for the whole process. A number is never reused, so a
	// session after a revoke and a new enrollment cannot match a stamp from the earlier session.
	lastGeneration uint64
	closed         bool
}

type liveRunner struct {
	client     *runner.Client
	generation uint64
}

func newRunners(q *sqlc.Queries) *Runners {
	return &Runners{q: q, sessions: map[string]liveRunner{}}
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

// Accept installs a ready connection after checking that its token is still valid.
// It returns when the session ends.
func (rt *Runners) Accept(ctx context.Context, id, token string, socket *websocket.Conn) error {
	session, err := runner.Connect(ctx, socket)
	if err != nil {
		return err
	}
	defer session.Close()
	rt.mu.Lock()
	err = rt.Verify(ctx, id, token)
	if err == nil && rt.closed {
		err = runner.ErrOffline
	}
	if _, live := rt.sessions[id]; err == nil && live {
		err = ErrRunnerConflict
	}
	if err == nil {
		rt.lastGeneration++
		rt.sessions[id] = liveRunner{client: session, generation: rt.lastGeneration}
	}
	rt.mu.Unlock()
	if err != nil {
		return err
	}
	defer func() {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if rt.sessions[id].client == session {
			delete(rt.sessions, id)
		}
	}()
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

// generation identifies the live session of a machine, or is 0 when the machine is offline.
// A reconnect changes it, so a stamp from the earlier session cannot match.
func (rt *Runners) generation(id string) uint64 {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.sessions[id].generation
}

func (rt *Runners) close() {
	rt.mu.Lock()
	rt.closed = true
	sessions := rt.sessions
	rt.sessions = nil
	rt.mu.Unlock()
	for _, session := range sessions {
		session.client.Close()
	}
}
