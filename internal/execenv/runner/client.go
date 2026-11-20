// Package runner implements a session between Dex and a remote execution host.
package runner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/MichaelKinsy/PiG/durable/env"
	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/execenv/runnerproto"
)

const maxFileBytes = 16 << 20
const messageTimeout = 10 * time.Second

var (
	ErrOffline   = errors.New("runner is offline")
	ErrUnknown   = errors.New("runner result unknown; inspect the target before retrying")
	errProtocol  = errors.New("unexpected runner response")
	errFileLimit = errors.New("runner file exceeds the 16 MiB session limit")
)

// Client owns one admitted socket. It never reconnects or replays a request.
// This session supports one operation at a time.
type Client struct {
	socket    *websocket.Conn
	wire      *runnerproto.Transport
	gate      chan struct{}
	inbox     chan runnerproto.Message
	done      chan struct{}
	closeOnce sync.Once
	cancel    context.CancelFunc
	next      uint64
	info      runnerproto.Ready
}

// Connect takes ownership of socket and waits for the runner to become ready.
// ctx bounds readiness only; Close controls the lifetime of the session.
func Connect(ctx context.Context, socket *websocket.Conn) (*Client, error) {
	ctx, cancel := context.WithTimeout(ctx, messageTimeout)
	defer cancel()
	wire, err := runnerproto.New(socket)
	if err != nil {
		_ = socket.CloseNow()
		return nil, err
	}
	msg, err := wire.Read(ctx)
	if err != nil || msg.Ready == nil {
		_ = socket.CloseNow()
		return nil, errors.Join(errProtocol, err)
	}
	lifetime, stop := context.WithCancel(context.WithoutCancel(ctx))
	c := &Client{socket: socket, wire: wire, gate: make(chan struct{}, 1), inbox: make(chan runnerproto.Message, 16), done: make(chan struct{}), cancel: stop, info: *msg.Ready}
	go c.receive(lifetime)
	go heartbeat(lifetime, socket, c.Close)
	return c, nil
}

func (c *Client) receive(ctx context.Context) {
	defer close(c.inbox)
	defer c.Close()
	for {
		msg, err := c.wire.Read(ctx)
		if err != nil {
			return
		}
		select {
		case c.inbox <- msg:
		case <-c.done:
			return
		}
	}
}

// Close interrupts pending reads and writes without waiting for the network.
func (c *Client) Close() {
	c.closeOnce.Do(func() { close(c.done); c.cancel(); _ = c.socket.CloseNow() })
}

// Done closes when the session can no longer accept work.
func (c *Client) Done() <-chan struct{} { return c.done }

func (c *Client) Info() runnerproto.Ready { return c.info }

func (c *Client) FileOperation(ctx context.Context, request runnerproto.Request, input []byte) ([]byte, error) {
	if len(input) > maxFileBytes {
		return nil, errFileLimit
	}
	data, _, err := c.call(ctx, request, input, nil)
	return data, err
}

func (c *Client) ReadFile(ctx context.Context, path string) ([]byte, error) {
	data, _, err := c.call(ctx, runnerproto.Request{Op: "read", Path: path}, nil, nil)
	return data, err
}

func (c *Client) WriteFile(ctx context.Context, path string, data []byte) error {
	if len(data) > maxFileBytes {
		return errFileLimit
	}
	_, _, err := c.call(ctx, runnerproto.Request{Op: "write", Path: path}, data, nil)
	return err
}

func (c *Client) Exec(ctx context.Context, command string, onOutput func(string, env.ShellStream)) (int, error) {
	return c.RunCommand(ctx, runnerproto.Request{Op: "exec", Command: command}, onOutput)
}

func (c *Client) RunCommand(ctx context.Context, request runnerproto.Request, onOutput func(string, env.ShellStream)) (int, error) {
	_, code, err := c.call(ctx, request, nil, onOutput)
	return code, err
}

func (c *Client) call(ctx context.Context, request runnerproto.Request, data []byte, onOutput func(string, env.ShellStream)) ([]byte, int, error) {
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	case <-c.done:
		return nil, 0, ErrOffline
	}
	select {
	case <-c.done:
		return nil, 0, ErrOffline
	default:
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	c.next++
	id := c.next
	if err := write(ctx, c.wire, runnerproto.Message{ID: id, Request: &request}); err != nil {
		if errors.Is(err, runnerproto.ErrInvalidMessage) {
			return nil, 0, err
		}
		c.Close()
		return nil, 0, errors.Join(ErrUnknown, err)
	}
	if request.Op == "write" || request.Op == "append" {
		if err := writeChunks(ctx, c.wire, id, data); err != nil {
			c.Close()
			return nil, 0, errors.Join(ErrUnknown, err)
		}
	}
	return c.result(ctx, id, request, onOutput)
}

func (c *Client) result(ctx context.Context, id uint64, request runnerproto.Request, onOutput func(string, env.ShellStream)) ([]byte, int, error) {
	op := request.Op
	var data []byte
	var ended bool
	ctxDone := ctx.Done()
	var cancelDeadline <-chan time.Time
	for {
		select {
		case msg, ok := <-c.inbox:
			if !ok {
				return nil, 0, ErrUnknown
			}
			if msg.ID != id {
				c.Close()
				return nil, 0, errors.Join(ErrUnknown, errProtocol)
			}
			switch {
			case msg.Result != nil:
				if failure := msg.Result.Error; failure != nil {
					if failure.Code == "canceled" && ctx.Err() != nil {
						return nil, 0, ctx.Err()
					}
					if op != "exec" {
						return nil, 0, env.NewFileError(env.FileErrorCode(failure.Code), failure.Message, request.Path, nil)
					}
					return nil, 0, errors.New(failure.Message)
				}
				if (op != "read" && len(data) == 0) || ended {
					return data, msg.Result.ExitCode, nil
				}
			case msg.Chunk != nil && op != "exec" && op != "write" && op != "append" && !ended && len(data)+len(msg.Chunk.Data) <= maxFileBytes:
				data = append(data, msg.Chunk.Data...)
				ended = msg.Chunk.End
				continue
			case msg.Output != nil && op == "exec":
				if onOutput != nil {
					onOutput(msg.Output.Text, env.ShellStream(msg.Output.Stream))
				}
				continue
			}
			c.Close()
			return nil, 0, errors.Join(ErrUnknown, errProtocol)
		case <-ctxDone:
			ctxDone = nil
			if err := write(context.WithoutCancel(ctx), c.wire, runnerproto.Message{ID: id, Cancel: new(runnerproto.Cancel)}); err != nil {
				// A final reply can already be in the read queue after the socket closed.
				c.Close()
			}
			timer := time.NewTimer(messageTimeout)
			defer timer.Stop()
			cancelDeadline = timer.C
		case <-cancelDeadline:
			c.Close()
			return nil, 0, ErrUnknown
		}
	}
}

func write(ctx context.Context, wire *runnerproto.Transport, msg runnerproto.Message) error {
	ctx, cancel := context.WithTimeout(ctx, messageTimeout)
	defer cancel()
	return wire.Write(ctx, msg)
}

func writeChunks(ctx context.Context, wire *runnerproto.Transport, id uint64, data []byte) error {
	for {
		n := min(len(data), runnerproto.MaxChunkBytes)
		end := n == len(data)
		if err := write(ctx, wire, runnerproto.Message{ID: id, Chunk: &runnerproto.Chunk{Data: data[:n], End: end}}); err != nil {
			return fmt.Errorf("send file chunk: %w", err)
		}
		if end {
			return nil
		}
		data = data[n:]
	}
}
