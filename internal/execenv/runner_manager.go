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

var ErrRunnerCredential = errors.New("runner credential is not valid")
var ErrRunnerConflict = errors.New("runner already paired or connected; revoke it before pairing another device")

type runnerRuntime struct {
	q        *sqlc.Queries
	mu       sync.Mutex
	sessions map[string]*runner.Client
	closed   bool
}

func runnerDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// IssueRunnerCode creates a ten-minute pairing code for a saved runner environment.
func (m *Manager) IssueRunnerCode(ctx context.Context, id string) (string, error) {
	code := rand.Text()
	rt := m.runner
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

// PairRunner consumes a one-time code and returns a new device credential.
func (m *Manager) PairRunner(ctx context.Context, id, code string) (string, error) {
	token := rand.Text()
	rt := m.runner
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

func (rt *runnerRuntime) verify(ctx context.Context, id, credential string) error {
	digest, err := rt.q.GetRunnerCredential(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrRunnerCredential
	}
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(digest), []byte(runnerDigest(credential))) != 1 {
		return ErrRunnerCredential
	}
	return nil
}

// VerifyRunner checks a device credential before the HTTP upgrade.
func (m *Manager) VerifyRunner(ctx context.Context, id, credential string) error {
	return m.runner.verify(ctx, id, credential)
}

// AcceptRunner installs a ready connection after checking that its credential is still valid.
func (m *Manager) AcceptRunner(ctx context.Context, id, credential string, socket *websocket.Conn) error {
	session, err := runner.Connect(ctx, socket)
	if err != nil {
		return err
	}
	defer session.Close()
	rt := m.runner
	rt.mu.Lock()
	err = rt.verify(ctx, id, credential)
	if err == nil && rt.closed {
		err = runner.ErrOffline
	}
	if err == nil && rt.sessions[id] != nil {
		err = ErrRunnerConflict
	}
	if err == nil {
		rt.sessions[id] = session
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

// RevokeRunner removes pairing state and disconnects the current device.
func (m *Manager) RevokeRunner(ctx context.Context, id string) error {
	rt := m.runner
	rt.mu.Lock()
	err := rt.q.RevokeRunnerCredential(ctx, id)
	var session *runner.Client
	if err == nil {
		session = rt.sessions[id]
		delete(rt.sessions, id)
	}
	rt.mu.Unlock()
	if session != nil {
		session.Close()
	}
	return err
}

func (rt *runnerRuntime) online(id string) *runner.Client {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.sessions[id]
}

func (rt *runnerRuntime) close() {
	rt.mu.Lock()
	rt.closed = true
	sessions := rt.sessions
	rt.sessions = nil
	rt.mu.Unlock()
	for _, session := range sessions {
		session.Close()
	}
}
