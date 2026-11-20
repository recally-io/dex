// Package runner implements a session between Dex and a remote execution host.
package runner

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MichaelKinsy/PiG/durable/env"
	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/execenv/runnerproto"
	"github.com/sourcegraph/jsonrpc2"
)

const maxFileBytes = 16 << 20
const messageTimeout = 10 * time.Second

var (
	ErrOffline   = errors.New("runner is offline")
	ErrUnknown   = errors.New("runner result unknown; inspect the target before retrying")
	errFileLimit = errors.New("runner file exceeds the 16 MiB session limit")
	errNoReady   = errors.New("the machine did not report itself ready")
)

// Client owns one admitted session. It never reconnects or replays a call, and it runs one
// call at a time.
type Client struct {
	conn   *jsonrpc2.Conn
	info   runnerproto.Ready
	done   chan struct{}
	stop   context.CancelFunc
	once   sync.Once
	nextID atomic.Uint64
	mu     sync.Mutex
	output func(tag uint64, text string, stream env.ShellStream)
}

// Connect takes ownership of socket and waits for the machine to report ready.
func Connect(ctx context.Context, socket *websocket.Conn) (*Client, error) {
	ready := make(chan runnerproto.Ready, 1)
	c := &Client{done: make(chan struct{})}
	lifetime, stop := context.WithCancel(context.WithoutCancel(ctx))
	c.stop = stop
	// The handler runs in the read loop, so output arrives before the reply that ends its call.
	c.conn = jsonrpc2.NewConn(lifetime, runnerproto.NewStream(lifetime, socket), &clientHandler{client: c, ready: ready})
	select {
	case c.info = <-ready:
	case <-c.conn.DisconnectNotify():
		stop()
		return nil, errNoReady
	case <-time.After(messageTimeout):
		stop()
		return nil, errNoReady
	}
	go func() {
		<-c.conn.DisconnectNotify()
		c.Close()
	}()
	// The socket reader answers pongs while the heartbeat waits for a reply.
	go heartbeat(lifetime, socket, c.Close)
	return c, nil
}

// Close interrupts pending calls without waiting for the network.
func (c *Client) Close() {
	c.once.Do(func() {
		close(c.done)
		c.stop()
		_ = c.conn.Close()
	})
}

// Done closes when the session can no longer accept work.
func (c *Client) Done() <-chan struct{} { return c.done }

// Info reports the platform and the workspace of the machine.
func (c *Client) Info() runnerproto.Ready { return c.info }

// Call runs one method and returns the machine answer. A canceled context stops the call on
// the machine, and a lost session reports an unknown result.
func (c *Client) Call(ctx context.Context, method string, params runnerproto.Tagger, result any) error {
	select {
	case <-c.done:
		return ErrOffline
	default:
	}
	tag := c.nextID.Add(1)
	params.SetTag(tag)
	return c.run(ctx, method, tag, params, result)
}

func (c *Client) run(ctx context.Context, method string, tag uint64, params, result any) error {
	err := c.conn.Call(ctx, method, params, result)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		// The machine keeps running the call until it hears the cancel.
		_ = c.conn.Notify(context.WithoutCancel(ctx), runnerproto.MethodCancel, runnerproto.CancelParams{Identity: runnerproto.IdentityOf(tag)})
		return ctx.Err()
	}
	if errors.Is(err, jsonrpc2.ErrClosed) {
		c.Close()
		return errors.Join(ErrUnknown, err)
	}
	return machineFailure(err)
}

// machineFailure turns a machine error into the execution environment error that asked for it.
func machineFailure(err error) error {
	var rpcErr *jsonrpc2.Error
	if !errors.As(err, &rpcErr) {
		return err
	}
	var detail runnerproto.Failure
	if rpcErr.Data != nil {
		_ = decode(rpcErr.Data, &detail)
	}
	return env.NewFileError(env.FileErrorCode(detail.Code), rpcErr.Message, detail.Path, nil)
}

// Exec runs one command and streams its output to onOutput.
func (c *Client) Exec(ctx context.Context, command any, opts runnerproto.ExecParams, onOutput func(string, env.ShellStream)) (int, error) {
	switch value := command.(type) {
	case string:
		opts.Shell = value
	case []string:
		opts.Argv = value
	}
	select {
	case <-c.done:
		return 0, ErrOffline
	default:
	}
	tag := c.nextID.Add(1)
	opts.SetTag(tag)
	if onOutput != nil {
		c.mu.Lock()
		c.output = func(call uint64, text string, stream env.ShellStream) {
			if call == tag {
				onOutput(text, stream)
			}
		}
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			c.output = nil
			c.mu.Unlock()
		}()
	}
	var result runnerproto.ExecResult
	if err := c.run(ctx, runnerproto.MethodExec, tag, &opts, &result); err != nil {
		return 0, err
	}
	return result.ExitCode, nil
}

// ReadFile reads one file from the machine.
func (c *Client) ReadFile(ctx context.Context, path string) ([]byte, error) {
	var result runnerproto.DataResult
	if err := c.Call(ctx, runnerproto.MethodRead, &runnerproto.PathParams{Path: path}, &result); err != nil {
		return nil, err
	}
	return result.Data, nil
}

// WriteFile writes one file on the machine.
func (c *Client) WriteFile(ctx context.Context, path string, data []byte) error {
	if len(data) > maxFileBytes {
		return errFileLimit
	}
	return c.Call(ctx, runnerproto.MethodWrite, &runnerproto.WriteParams{Path: path, Data: data}, nil)
}

// clientHandler receives the messages that the machine sends on its own.
type clientHandler struct {
	client *Client
	ready  chan<- runnerproto.Ready
}

func (h *clientHandler) Handle(_ context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) {
	switch req.Method {
	case runnerproto.MethodReady:
		var info runnerproto.Ready
		if req.Params == nil || decode(req.Params, &info) != nil {
			return
		}
		select {
		case h.ready <- info:
		default:
		}
	case runnerproto.MethodOutput:
		var output runnerproto.OutputParams
		if req.Params == nil || decode(req.Params, &output) != nil {
			return
		}
		h.client.mu.Lock()
		deliver := h.client.output
		h.client.mu.Unlock()
		if deliver != nil {
			deliver(output.Tag, output.Text, env.ShellStream(output.Stream))
		}
	}
}
