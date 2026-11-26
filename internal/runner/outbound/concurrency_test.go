package outbound_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/durable/env"
	"github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/runner/outbound"
	"github.com/sourcegraph/jsonrpc2"
)

func TestUserGetsOnlyTheirCommandOutputAndCancellation(t *testing.T) {
	client, _ := connect(t)
	ctx, deadline := context.WithTimeout(t.Context(), 10*time.Second)
	defer deadline()
	a, cancelA := context.WithCancel(ctx)
	defer cancelA()
	started := make(chan string, 8)
	doneA, doneB := make(chan error, 1), make(chan error, 1)
	start := func(ctx context.Context, label, command string, wantCode int, done chan<- error) {
		var out, stderr strings.Builder
		code, err := client.Exec(ctx, command, outbound.ExecParams{}, func(text string, stream env.ShellStream) {
			switch stream {
			case env.ShellStdout:
				out.WriteString(text)
			case env.ShellStderr:
				stderr.WriteString(text)
			}
			started <- label + ":" + string(stream)
		})
		if code != wantCode || out.String() != label+"-out\n" || stderr.String() != label+"-err\n" {
			t.Errorf("%s returned (%d, %q, %q, %v)", label, code, out.String(), stderr.String(), err)
		}
		done <- err
	}
	go start(a, "A", `printf 'A-out\n'; printf 'A-err\n' >&2; exec sleep 60`, 0, doneA)
	go start(ctx, "B", `printf 'B-out\n'; printf 'B-err\n' >&2; while [ ! -e finish ]; do sleep .05; done; exit 7`, 7, doneB)
	seen := make(map[string]bool)
	for range 4 {
		seen[receive(ctx, t, started)] = true
	}
	if len(seen) != 4 {
		t.Fatalf("expected both streams from each command; got %v", seen)
	}
	cancelA()
	if err := receive(ctx, t, doneA); !errors.Is(err, context.Canceled) {
		t.Fatalf("first command returned %v", err)
	}
	if err := client.WriteFile(ctx, "finish", nil); err != nil {
		t.Fatal(err)
	}
	if err := receive(ctx, t, doneB); err != nil {
		t.Fatalf("second command was affected by cancellation: %v", err)
	}
}

func TestUserWaitsForAFreeRunnerSlotAndCanCancelTheWait(t *testing.T) {
	client, _ := connect(t)
	ctx, deadline := context.WithTimeout(t.Context(), 10*time.Second)
	defer deadline()
	starts, finishes := make(chan struct{}, 16), make(chan error, 16)
	stops := make([]context.CancelFunc, 0, 16)
	for range 16 {
		callCtx, stop := context.WithCancel(ctx)
		defer stop()
		stops = append(stops, stop)
		go func() {
			_, err := client.Exec(callCtx, "printf started; exec sleep 60", outbound.ExecParams{}, func(_ string, _ env.ShellStream) {
				starts <- struct{}{}
			})
			finishes <- err
		}()
	}
	for range 16 {
		receive(ctx, t, starts)
	}
	queued, stopQueued := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stopQueued()
	if _, err := client.Exec(queued, "touch should-not-run", outbound.ExecParams{}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued command returned %v", err)
	}
	stops[0]()
	if err := receive(ctx, t, finishes); !errors.Is(err, context.Canceled) {
		t.Fatalf("running command returned %v", err)
	}
	code, err := client.Exec(ctx, "test ! -e should-not-run", outbound.ExecParams{}, nil)
	if err != nil || code != 0 {
		t.Fatalf("replacement call returned %d, %v", code, err)
	}
}

func TestUserSlowOutputFailsOnlyItsOwnCommand(t *testing.T) {
	client, _ := connect(t)
	ctx, deadline := context.WithTimeout(t.Context(), 10*time.Second)
	defer deadline()
	if err := client.WriteFile(ctx, "short", []byte("ready")); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}, 1), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	finished := make(chan error, 1)
	go func() {
		_, err := client.Exec(ctx, `printf '%s' "$$" > pid; printf 'start\n'; while [ ! -e flood ]; do sleep .05; done; head -c 8388608 /dev/zero | tr '\000' x; exec sleep 60`, outbound.ExecParams{}, func(_ string, _ env.ShellStream) {
			started <- struct{}{}
			<-release
		})
		finished <- err
	}()
	receive(ctx, t, started)
	// The callback remains blocked. Neither a read nor canceling the flooded
	// command must depend on that consumer making progress.
	if err := client.WriteFile(ctx, "flood", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := client.ReadFile(ctx, "short"); err != nil || string(got) != "ready" {
		t.Fatalf("read behind blocked callback: %q, %v", got, err)
	}
	// The shell PID must exit even while its callback is blocked.
	if code, err := client.Exec(ctx, `pid=$(cat pid); while kill -0 "$pid" 2>/dev/null; do sleep .05; done`, outbound.ExecParams{}, nil); err != nil || code != 0 {
		t.Fatalf("flooded command did not stop: %d, %v", code, err)
	}
	close(release)
	released = true
	if err := receive(ctx, t, finished); err == nil || !strings.Contains(err.Error(), "could not keep up") {
		t.Fatalf("expected a visible backpressure failure, got %v", err)
	}
}

// wireHandler keeps the wire fixture's state in jsonrpc2's read loop.
type wireHandler func(context.Context, *jsonrpc2.Conn, *jsonrpc2.Request)

func (fn wireHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	fn(ctx, conn, req)
}

func TestUserRunnerCapsRawCallsAndSendsStartBeforeOutput(t *testing.T) {
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	work := t.TempDir()
	served := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(served)
		socket, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{outbound.Version}})
		if err != nil {
			t.Error(err)
			return
		}
		if err := outbound.Serve(ctx, socket, node.NewNodeExecutionEnv(node.NodeExecutionEnvOptions{Cwd: work})); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	socket, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{Subprotocols: []string{outbound.Version}}) //nolint:bodyclose // Dial owns the successful upgrade.
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan string, 64)
	conn := jsonrpc2.NewConn(ctx, outbound.NewStream(ctx, socket), wireHandler(func(_ context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) {
		events <- req.Method
	}))
	defer func() {
		_ = conn.Close()
		receive(ctx, t, served)
	}()
	if event := receive(ctx, t, events); event != outbound.MethodReady {
		t.Fatalf("first event was %q", event)
	}
	var waiters []jsonrpc2.Waiter
	for tag := uint64(1); tag <= 16; tag++ {
		waiter, err := conn.DispatchCall(ctx, outbound.MethodExec, outbound.ExecParams{Tag: tag, Shell: "printf started; exec sleep 60"})
		if err != nil {
			t.Fatal(err)
		}
		waiters = append(waiters, waiter)
		if got := receive(ctx, t, events); got != outbound.MethodStarted {
			t.Fatalf("call %d should get start before output; first event %q", tag, got)
		}
		if got := receive(ctx, t, events); got != outbound.MethodOutput {
			t.Fatalf("call %d expected output, got %q", tag, got)
		}
	}
	params := outbound.ExecParams{Tag: 17, Shell: "touch should-not-run"}
	if err := conn.Call(ctx, outbound.MethodExec, params, nil); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("raw 17th call should be rejected, got %v", err)
	}
	if err := conn.Notify(ctx, outbound.MethodCancel, outbound.CancelParams{Tag: 2}); err != nil {
		t.Fatal(err)
	}
	if err := waiters[1].Wait(ctx, nil); err == nil || ctx.Err() != nil {
		t.Fatalf("cancel did not get a remote failure: %v", err)
	}
	params.Shell = "test ! -e should-not-run"
	var result outbound.ExecResult
	if err := conn.Call(ctx, outbound.MethodExec, params, &result); err != nil || result.ExitCode != 0 {
		t.Fatalf("raw replacement call returned %d, %v", result.ExitCode, err)
	}
}
