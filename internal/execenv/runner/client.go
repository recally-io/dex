// Package runner implements a session between Dex and a remote execution host.
package runner

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/MichaelKinsy/PiG/durable/env"
	"github.com/coder/websocket"
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
	info   Ready
	done   chan struct{}
	stop   context.CancelFunc
	once   sync.Once
	gate   chan struct{}
	nextID uint64 // guarded by gate
	mu     sync.Mutex
	output func(tag uint64, text string, stream env.ShellStream)
}

// Connect takes ownership of socket and waits for the machine to report ready.
func Connect(ctx context.Context, socket *websocket.Conn) (*Client, error) {
	ready := make(chan Ready, 1)
	c := &Client{done: make(chan struct{}), gate: make(chan struct{}, 1)}
	lifetime, stop := context.WithCancel(context.WithoutCancel(ctx))
	c.stop = stop
	// The handler runs in the read loop, so output arrives before the reply that ends its call.
	c.conn = jsonrpc2.NewConn(lifetime, NewStream(lifetime, socket), &clientHandler{client: c, ready: ready})
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
func (c *Client) Info() Ready { return c.info }

// Call runs one method and returns the machine answer. A canceled context stops the call on
// the machine, and a lost session reports an unknown result.
func (c *Client) Call(ctx context.Context, method string, params Tagger, result any) error {
	return c.run(ctx, method, params, result, nil)
}

func (c *Client) run(ctx context.Context, method string, params Tagger, result any, onOutput func(string, env.ShellStream)) error {
	select {
	case <-c.done:
		return ErrOffline
	case <-ctx.Done():
		return ctx.Err()
	case c.gate <- struct{}{}:
	}
	defer func() { <-c.gate }()
	select {
	case <-c.done:
		return ErrOffline
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	c.nextID++
	tag := c.nextID
	params.SetTag(tag)
	if onOutput != nil {
		c.mu.Lock()
		c.output = func(call uint64, text string, stream env.ShellStream) {
			if call == tag && ctx.Err() == nil {
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

	waiter, err := c.conn.DispatchCall(ctx, method, params)
	if err == nil {
		err = waiter.Wait(ctx, result)
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			// Hold the execution slot until the machine stops the canceled operation.
			settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), messageTimeout)
			defer cancel()
			if notifyErr := c.conn.Notify(settle, MethodCancel, CancelParams{Identity: IdentityOf(tag)}); notifyErr != nil {
				c.Close()
			} else if waitErr := waiter.Wait(settle, nil); errors.Is(waitErr, context.DeadlineExceeded) {
				c.Close()
			}
			return err
		}
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
	var detail Failure
	if rpcErr.Data != nil {
		_ = decode(rpcErr.Data, &detail)
	}
	return env.NewFileError(env.FileErrorCode(detail.Code), rpcErr.Message, detail.Path, nil)
}

// Exec runs one command and streams its output to onOutput.
func (c *Client) Exec(ctx context.Context, command any, opts ExecParams, onOutput func(string, env.ShellStream)) (int, error) {
	switch value := command.(type) {
	case string:
		opts.Shell = value
	case []string:
		opts.Argv = value
	}
	var result ExecResult
	if err := c.run(ctx, MethodExec, &opts, &result, onOutput); err != nil {
		return 0, err
	}
	return result.ExitCode, nil
}

// ReadFile reads one file from the machine.
func (c *Client) ReadFile(ctx context.Context, path string) ([]byte, error) {
	var result DataResult
	if err := c.Call(ctx, MethodRead, &PathParams{Path: path}, &result); err != nil {
		return nil, err
	}
	return result.Data, nil
}

// WriteFile writes one file on the machine.
func (c *Client) WriteFile(ctx context.Context, path string, data []byte) error {
	if len(data) > maxFileBytes {
		return errFileLimit
	}
	return c.Call(ctx, MethodWrite, &WriteParams{Path: path, Data: data}, nil)
}

// clientHandler receives the messages that the machine sends on its own.
type clientHandler struct {
	client *Client
	ready  chan<- Ready
}

func (h *clientHandler) Handle(_ context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) {
	switch req.Method {
	case MethodReady:
		var info Ready
		if req.Params == nil || decode(req.Params, &info) != nil {
			return
		}
		select {
		case h.ready <- info:
		default:
		}
	case MethodOutput:
		var output OutputParams
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
