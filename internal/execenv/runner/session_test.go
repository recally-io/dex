package runner_test

import (
	"bytes"
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
	"github.com/recally-io/dex/internal/execenv/runner"
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
	result, err := client.Exec(ctx, "printf out; printf err >&2; exit 7", runner.ExecParams{}, func(text string, stream env.ShellStream) {
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
	_, err := client.Exec(runCtx, "printf started; exec sleep 60", runner.ExecParams{}, func(text string, _ env.ShellStream) {
		if strings.Contains(text, "started") {
			stop()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel returned %v", err)
	}
	result, err := client.Exec(ctx, "exit 3", runner.ExecParams{}, nil)
	if err != nil || result != 3 {
		t.Fatalf("next exec returned (%d, %v)", result, err)
	}
}

func TestUserWaitsForTheMachineBeforeRunningAnotherCommand(t *testing.T) {
	client, _ := connect(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	firstCtx, stopFirst := context.WithCancel(ctx)
	defer stopFirst()
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, err := client.Exec(firstCtx, "printf started; exec sleep 60", runner.ExecParams{}, func(_ string, _ env.ShellStream) {
			close(started)
		})
		finished <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("first command did not start")
	}

	// The second command cannot start while the first holds the machine.
	queued, stopQueued := context.WithTimeout(ctx, 500*time.Millisecond)
	defer stopQueued()
	if _, err := client.Exec(queued, "touch queued", runner.ExecParams{}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued command should time out without running, got %v", err)
	}
	stopFirst()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("first command did not cancel: %v", err)
	}
	var output strings.Builder
	code, err := client.Exec(ctx, "test ! -e queued && printf next", runner.ExecParams{}, func(text string, _ env.ShellStream) {
		output.WriteString(text)
	})
	if err != nil || code != 0 || output.String() != "next" {
		t.Fatalf("next command returned (%d, %q, %v)", code, output.String(), err)
	}
}

func TestUserSeesUnknownResultAfterRunnerDisconnects(t *testing.T) {
	client, stopped := connect(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err := client.Exec(ctx, "printf started; exec sleep 60", runner.ExecParams{}, func(text string, _ env.ShellStream) {
		if strings.Contains(text, "started") {
			client.Close()
		}
	})
	if !errors.Is(err, runner.ErrUnknown) {
		t.Fatalf("disconnect returned %v", err)
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("runner did not finish cleanup")
	}
	_, err = client.Exec(ctx, "exit 0", runner.ExecParams{}, nil)
	if !errors.Is(err, runner.ErrOffline) {
		t.Fatalf("new command on old session returned %v", err)
	}
}

func connect(t *testing.T) (*runner.Client, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	accepted := make(chan *runner.Client, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{runner.Version}})
		if err != nil {
			t.Error(err)
			return
		}
		session, err := runner.Connect(ctx, c)
		if err != nil {
			t.Error(err)
			return
		}
		accepted <- session
	}))
	t.Cleanup(server.Close)
	// Dial takes ownership of the socket and clears the HTTP response body.
	socket, _, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(server.URL, "https"), &websocket.DialOptions{HTTPClient: server.Client(), Subprotocols: []string{runner.Version}}) //nolint:bodyclose
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = runner.Serve(ctx, socket, node.NewNodeExecutionEnv(node.NodeExecutionEnvOptions{Cwd: work}))
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
