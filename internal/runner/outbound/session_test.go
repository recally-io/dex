package outbound_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/durable/env"
	"github.com/MichaelKinsy/PiG/durable/env/node"
	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/runner/outbound"
)

func TestUserRunsToolsOnConnectedRunner(t *testing.T) {
	client, _ := connect(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	payload := bytes.Repeat([]byte{0, 255, 10}, 1<<20)
	if err := client.WriteFile(ctx, "file", payload); err != nil {
		t.Fatal(err)
	}
	got, err := client.ReadFile(ctx, "file")
	if err != nil || !bytes.Equal(payload, got) {
		t.Fatalf("read returned %d bytes: %v", len(got), err)
	}
	var stdout, stderr strings.Builder
	result, err := client.Exec(ctx, "printf out; printf err >&2; exit 7", outbound.ExecParams{}, func(text string, stream env.ShellStream) {
		if stream == env.ShellStdout {
			stdout.WriteString(text)
		} else {
			stderr.WriteString(text)
		}
	})
	if err != nil || result != 7 || stdout.String() != "out" || stderr.String() != "err" {
		t.Fatalf("exec returned (%d, %q, %q, %v)", result, stdout.String(), stderr.String(), err)
	}
}

func TestUserCancelsCommandWithoutDisconnectingRunner(t *testing.T) {
	client, _ := connect(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	_, err := client.Exec(runCtx, "printf started; exec sleep 60", outbound.ExecParams{}, func(text string, _ env.ShellStream) {
		if strings.Contains(text, "started") {
			stop()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel returned %v", err)
	}
	result, err := client.Exec(ctx, "exit 3", outbound.ExecParams{}, nil)
	if err != nil || result != 3 {
		t.Fatalf("next exec returned (%d, %v)", result, err)
	}
}

func TestUserReadsFileWhileAnotherRunnerCommandWaits(t *testing.T) {
	// Given a file and a command that cannot finish until canceled.
	client, _ := connect(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := client.WriteFile(ctx, "file", []byte("ready")); err != nil {
		t.Fatal(err)
	}
	firstCtx, stopFirst := context.WithCancel(ctx)
	defer stopFirst()
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, err := client.Exec(firstCtx, "printf started; exec sleep 60", outbound.ExecParams{}, func(_ string, _ env.ShellStream) {
			close(started)
		})
		finished <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("first command did not start")
	}

	// When another call reads the file, it finishes before the command is canceled.
	readCtx, stopRead := context.WithTimeout(ctx, 2*time.Second)
	defer stopRead()
	if got, err := client.ReadFile(readCtx, "file"); err != nil || string(got) != "ready" {
		t.Fatalf("read while command is running returned %q, %v", got, err)
	}
	stopFirst()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("first command did not cancel: %v", err)
	}
}

func TestUserSeesUnknownResultsAndProcessesStopAfterRunnerDisconnects(t *testing.T) {
	client, stopped := connect(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	pids := make(chan int, 2)
	finished := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := client.Exec(ctx, `printf '%s\n' "$$"; exec sleep 60`, outbound.ExecParams{}, func(text string, _ env.ShellStream) {
				pid, err := strconv.Atoi(strings.TrimSpace(text))
				if err != nil {
					t.Error(err)
				}
				pids <- pid
			})
			finished <- err
		}()
	}
	first, second := receive(ctx, t, pids), receive(ctx, t, pids)
	client.Close()
	for range 2 {
		if err := receive(ctx, t, finished); !errors.Is(err, outbound.ErrUnknown) {
			t.Fatalf("disconnect returned %v", err)
		}
	}
	receive(ctx, t, stopped)
	for _, pid := range []int{first, second} {
		if pid <= 0 {
			t.Fatal("invalid child PID")
		}
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("command PID %d still present after cleanup: %v", pid, err)
		}
	}
	_, err := client.Exec(ctx, "exit 0", outbound.ExecParams{}, nil)
	if !errors.Is(err, outbound.ErrOffline) {
		t.Fatalf("new command on old session returned %v", err)
	}
}

func connect(t *testing.T) (*outbound.Client, <-chan struct{}) {
	t.Helper()
	return connectWith(t, func(ctx context.Context, socket *websocket.Conn, work string) {
		_ = outbound.Serve(ctx, socket, node.NewNodeExecutionEnv(node.NodeExecutionEnvOptions{Cwd: work}))
	})
}

func connectWith(t *testing.T, serve func(context.Context, *websocket.Conn, string)) (*outbound.Client, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	accepted := make(chan *outbound.Client, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{outbound.Version}})
		if err != nil {
			t.Error(err)
			return
		}
		session, err := outbound.Connect(ctx, c, "test-runner")
		if err != nil {
			t.Error(err)
			return
		}
		accepted <- session
	}))
	t.Cleanup(server.Close)
	// Dial takes ownership of the socket and clears the HTTP response body.
	socket, _, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(server.URL, "https"), &websocket.DialOptions{HTTPClient: server.Client(), Subprotocols: []string{outbound.Version}}) //nolint:bodyclose
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		serve(ctx, socket, work)
	}()
	t.Cleanup(func() { _ = socket.CloseNow(); <-stopped })
	select {
	case client := <-accepted:
		t.Cleanup(client.Close)
		return client, stopped
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return nil, nil
	}
}

func receive[T any](ctx context.Context, t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		var zero T
		return zero
	}
}
