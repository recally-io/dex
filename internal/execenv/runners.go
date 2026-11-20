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
	sessions map[string]*runner.Client
	// generations counts the sessions of one environment, so a reconnect changes its stamp.
	generations map[string]uint64
	closed      bool
}

func newRunners(q *sqlc.Queries) *Runners {
	return &Runners{q: q, sessions: map[string]*runner.Client{}, generations: map[string]uint64{}}
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
	if err == nil && rt.sessions[id] != nil {
		err = ErrRunnerConflict
	}
	if err == nil {
		rt.sessions[id] = session
		rt.generations[id]++
	}
	rt.mu.Unlock()
	if err != nil {
		return err
	}
	defer func() {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if rt.sessions[id] == session {
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
		session = rt.sessions[id]
		delete(rt.sessions, id)
		delete(rt.generations, id)
	}
	rt.mu.Unlock()
	if session != nil {
		session.Close()
	}
	return err
}

func (rt *Runners) online(id string) *runner.Client {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.sessions[id]
}

// generation counts the sessions of a machine. A reconnect changes it, so a stamp from the
// earlier session cannot match.
func (rt *Runners) generation(id string) uint64 {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.generations[id]
}

func (rt *Runners) close() {
	rt.mu.Lock()
	rt.closed = true
	sessions := rt.sessions
	rt.sessions = nil
	rt.mu.Unlock()
	for _, session := range sessions {
		session.Close()
	}
}
