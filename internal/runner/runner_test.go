package runner_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	"github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/runner"
	"github.com/recally-io/dex/internal/runner/outbound"
)

// machineFixture is a Manager with one enrolled machine that a bot uses as its default.
type machineFixture struct {
	t     *testing.T
	mgr   *runner.Manager
	q     *sqlc.Queries
	url   string
	token string
	work  string
}

func newMachineFixture(t *testing.T) *machineFixture {
	t.Helper()
	conn, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	q := sqlc.New(conn)
	now := time.Now().UnixMilli()
	if _, err := q.CreateEnvironment(t.Context(), sqlc.CreateEnvironmentParams{ID: "laptop", Name: "Laptop", Kind: string(runner.KindRunner), Config: "{}", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateAgent(t.Context(), sqlc.CreateAgentParams{ID: "coder", Name: "Coder", ThinkingLevel: "off", EnvironmentID: "laptop", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	mgr := runner.NewManager(runner.ManagerConfig{DataDir: t.TempDir(), Queries: q})
	t.Cleanup(func() { _ = mgr.Close(context.Background()) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{outbound.Version}})
		if err != nil {
			return
		}
		_ = mgr.Runners().Accept(r.Context(), "laptop", token, c)
	}))
	t.Cleanup(server.Close)
	f := &machineFixture{t: t, mgr: mgr, q: q, url: "ws" + strings.TrimPrefix(server.URL, "http"), work: t.TempDir()}
	f.enroll()
	return f
}

// enroll pairs a new machine credential and returns it.
func (f *machineFixture) enroll() {
	f.t.Helper()
	code, err := f.mgr.Runners().IssueCode(f.t.Context(), "laptop")
	if err != nil {
		f.t.Fatal(err)
	}
	if f.token, err = f.mgr.Runners().Enroll(f.t.Context(), "laptop", code); err != nil {
		f.t.Fatal(err)
	}
}

// connect starts one machine session and waits until Dex reports it. The returned function
// drops the socket without a close handshake.
func (f *machineFixture) connect() func() {
	f.t.Helper()
	header := http.Header{"Authorization": []string{"Bearer " + f.token}}
	socket, _, err := websocket.Dial(f.t.Context(), f.url, &websocket.DialOptions{HTTPHeader: header, Subprotocols: []string{outbound.Version}}) //nolint:bodyclose
	if err != nil {
		f.t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = outbound.Serve(context.WithoutCancel(f.t.Context()), socket, node.NewNodeExecutionEnv(node.NodeExecutionEnvOptions{Cwd: f.work}))
	}()
	drop := func() { _ = socket.CloseNow(); <-served }
	f.t.Cleanup(drop)
	f.waitStatus("available")
	return drop
}

func (f *machineFixture) waitStatus(want string) {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for got := ""; got != want; got = f.mgr.Status(f.t.Context(), "coder", "") {
		if time.Now().After(deadline) {
			f.t.Fatalf("machine status %q, want %q", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *machineFixture) stamp() string {
	f.t.Helper()
	stamp, err := f.mgr.Stamp(f.t.Context(), "coder", "")
	if err != nil {
		f.t.Fatal(err)
	}
	return stamp
}

func run(ctx context.Context, h runner.Handle, command string) (string, error) {
	e, err := h.Env(ctx, h.Workspace(), nil)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	_, err = e.Exec(ctx, command, &durableenv.ShellExecOptions{OnOutput: func(_ context.Context, text string, _ durableenv.ShellOutputInfo) {
		out.WriteString(text)
	}})
	return out.String(), err
}

func TestUserMachineReconnectKeepsTheBotLease(t *testing.T) {
	// Given a bot lease on a connected machine.
	f := newMachineFixture(t)
	drop := f.connect()
	h, err := f.mgr.Open(t.Context(), "coder", "")
	if err != nil {
		t.Fatal(err)
	}
	before := h.Stamp()

	// When the machine loses its session, Dex reports it offline without a stale stamp.
	drop()
	f.waitStatus("offline")
	if f.stamp() != before {
		t.Fatal("a lost session changed the stamp")
	}
	// A call that starts while the machine is offline waits for the next session.
	result := make(chan error, 1)
	var out string
	go func() {
		var err error
		out, err = run(t.Context(), h, "printf again")
		result <- err
	}()
	f.connect()

	// Then the same lease runs the call on the new session, and the stamp stays the same.
	select {
	case err := <-result:
		if err != nil || out != "again" {
			t.Fatalf("call across the reconnect: %q, %v", out, err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the call did not use the new session")
	}
	if f.stamp() != before || h.Stamp() != before {
		t.Fatal("a reconnect changed the stamp")
	}
}

func TestUserMachineReconnectLogsRejectedWorkspace(t *testing.T) {
	// Given a Bot lease on the original workspace.
	f := newMachineFixture(t)
	drop := f.connect()
	h, err := f.mgr.Open(t.Context(), "coder", "")
	if err != nil {
		t.Fatal(err)
	}
	original := f.work

	// When the same machine reconnects with a different workspace.
	drop()
	f.waitStatus("offline")
	f.work = t.TempDir()
	f.connect()
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	_, err = run(t.Context(), h, "private-command")
	slog.SetDefault(old)
	if !errors.Is(err, runner.ErrUnavailable) {
		t.Fatalf("call after workspace changed: %v", err)
	}

	// Then the trace reports a correlated failure without a send or private data.
	raw := logs.String()
	if strings.Contains(raw, original) || strings.Contains(raw, f.work) || strings.Contains(raw, "private-command") {
		t.Fatal("Runner trace contains private data")
	}
	var waitID string
	finished := false
	for line := range strings.SplitSeq(strings.TrimSpace(raw), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] != "runner RPC" || record["environment"] != "laptop" || record["method"] != outbound.MethodExec {
			continue
		}
		switch record["phase"] {
		case "connection_wait":
			waitID, _ = record["call_id"].(string)
		case "finished":
			if waitID == "" || record["call_id"] != waitID || record["result"] != "failed" || record["reason"] != "workspace_mismatch" || record["elapsed"] == nil || record["sent"] != false {
				t.Fatalf("invalid terminal event: %v", record)
			}
			finished = true
		case "sent", "remote_started":
			t.Fatalf("rejected call reached dispatch: %v", record)
		}
	}
	if !finished {
		t.Fatal("missing terminal event for rejected workspace")
	}
}

func TestUserMachineReconnectReplacesAHalfOpenSession(t *testing.T) {
	// Given a lease that runs a long command on a session that Dex still holds.
	f := newMachineFixture(t)
	f.connect()
	h, err := f.mgr.Open(t.Context(), "coder", "")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := run(t.Context(), h, "touch started; sleep 60"); result <- err }()
	started := filepath.Join(h.Workspace(), "started")
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the long command did not start")
		}
	}

	// When the machine connects again with its token before Dex sees the first session end.
	f.connect()

	// Then the new session replaces the first one at once, the dispatched command reports an
	// unknown result instead of a replay, and the lease runs new work on the new session.
	select {
	case err := <-result:
		if !errors.Is(err, outbound.ErrUnknown) {
			t.Fatalf("dispatched command after the replacement: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first session stayed open after the machine connected again")
	}
	if out, err := run(t.Context(), h, "printf new"); err != nil || out != "new" {
		t.Fatalf("call on the new session: %q, %v", out, err)
	}
}

func TestUserMachineRevokeAndNewEnrollmentEndTheLease(t *testing.T) {
	// Given a bot lease on a connected machine.
	f := newMachineFixture(t)
	f.connect()
	h, err := f.mgr.Open(t.Context(), "coder", "")
	if err != nil {
		t.Fatal(err)
	}
	before := h.Stamp()

	// When the user revokes the machine.
	if err := f.mgr.Runners().Revoke(t.Context(), "laptop"); err != nil {
		t.Fatal(err)
	}

	// Then the stamp changes, and the lease fails at once without the reconnect wait.
	revoked := f.stamp()
	if revoked == before {
		t.Fatal("a revoke kept the stamp")
	}
	start := time.Now()
	if _, err := run(t.Context(), h, "true"); !errors.Is(err, runner.ErrUnavailable) || time.Since(start) > 2*time.Second {
		t.Fatalf("call on a revoked lease after %s: %v", time.Since(start), err)
	}

	// When the machine enrolls again and connects with the new token.
	f.enroll()
	f.connect()

	// Then the stamp changes again, the old lease stays unusable, and a new lease works.
	if now := f.stamp(); now == before || now == revoked {
		t.Fatal("a new enrollment reused an earlier stamp")
	}
	if _, err := run(t.Context(), h, "true"); !errors.Is(err, runner.ErrUnavailable) {
		t.Fatalf("old lease on the new enrollment: %v", err)
	}
	fresh, err := f.mgr.Open(t.Context(), "coder", "")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := run(t.Context(), fresh, "printf fresh"); err != nil || out != "fresh" {
		t.Fatalf("new lease: %q, %v", out, err)
	}
}

func TestUserMachineRevokeEndsAWaitingCall(t *testing.T) {
	// Given a lease on an offline machine and a call that waits for the machine.
	f := newMachineFixture(t)
	drop := f.connect()
	h, err := f.mgr.Open(t.Context(), "coder", "")
	if err != nil {
		t.Fatal(err)
	}
	drop()
	f.waitStatus("offline")
	result := make(chan error, 1)
	go func() { _, err := run(t.Context(), h, "true"); result <- err }()
	time.Sleep(100 * time.Millisecond)

	// When the user revokes the machine during the wait.
	start := time.Now()
	if err := f.mgr.Runners().Revoke(t.Context(), "laptop"); err != nil {
		t.Fatal(err)
	}

	// Then the call fails at once instead of at the end of the reconnect wait.
	select {
	case err := <-result:
		if !errors.Is(err, runner.ErrUnavailable) || errors.Is(err, outbound.ErrOffline) {
			t.Fatalf("waiting call after the revoke: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting call did not end within 2 s of the revoke")
	}
	t.Logf("artifact: the waiting call ended %s after the revoke", time.Since(start))
}
