// Package runner implements a session between Dex and a remote execution host.
package runner

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MichaelKinsy/PiG/durable/env"
	"github.com/coder/websocket"
	"github.com/sourcegraph/jsonrpc2"
)

const maxFileBytes = 16 << 20
const messageTimeout = 10 * time.Second

const (
	maxOutputChunks = 256
	maxOutputBytes  = 2 << 20
)

var (
	ErrOffline            = errors.New("runner is offline")
	ErrUnknown            = errors.New("runner result unknown; inspect the target before retrying")
	errFileLimit          = errors.New("runner file exceeds the 16 MiB session limit")
	errNoReady            = errors.New("the machine did not report itself ready")
	errOutputBackpressure = errors.New("runner output consumer could not keep up; command canceled")
)

// Client owns one admitted session. It never reconnects or replays a call.
type Client struct {
	conn        *jsonrpc2.Conn
	info        Ready
	environment string
	done        chan struct{}
	stop        context.CancelFunc
	once        sync.Once
	gate        chan struct{}
	nextID      atomic.Uint64
	mu          sync.Mutex
	calls       map[uint64]*clientCall
}

// Connect takes ownership of socket and waits for the machine to report ready.
func Connect(ctx context.Context, socket *websocket.Conn, environment string) (*Client, error) {
	ready := make(chan Ready, 1)
	c := &Client{environment: environment, done: make(chan struct{}), calls: make(map[uint64]*clientCall)}
	lifetime, stop := context.WithCancel(context.WithoutCancel(ctx))
	c.stop = stop
	// The handler runs in the read loop, so output arrives before the reply that ends its call.
	c.conn = jsonrpc2.NewConn(lifetime, NewStream(lifetime, socket), &clientHandler{client: c, ready: ready})
	select {
	case c.info = <-ready:
		c.gate = make(chan struct{}, min(max(c.info.MaxInFlight, 1), maxInFlight))
	case <-ctx.Done():
		c.Close()
		return nil, ctx.Err()
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

func (c *Client) run(ctx context.Context, method string, params Tagger, result any, onOutput func(string, env.ShellStream)) (callErr error) {
	trace, ok := ctx.Value(callTraceKey{}).(callTrace)
	if !ok {
		trace = newCallTrace(c.environment, method)
	}
	call := &clientCall{ctx: ctx, since: time.Now(), trace: trace}
	var tag uint64
	var slotWait, sentAfter time.Duration
	var sent bool
	var cancelSettlement string
	defer func() {
		elapsed := time.Since(call.since)
		if tag == 0 && slotWait == 0 {
			slotWait = elapsed // the call ended without acquiring a slot
		}
		outcome := callOutcome(callErr)
		if exec, ok := result.(*ExecResult); callErr == nil && ok && exec.ExitCode != 0 {
			outcome = "exit_nonzero"
		}
		attrs := []any{"tag", tag, "slot_wait", slotWait, "elapsed", elapsed, "result", outcome, "sent", sent}
		if sent {
			attrs = append(attrs, "sent_after", sentAfter)
		}
		if cancelSettlement != "" {
			attrs = append(attrs, "cancel_settlement", cancelSettlement)
		}
		if started := call.started.Load(); started != nil {
			attrs = append(attrs, "remote_start_observed_after", started.Sub(call.since))
		}
		trace.log(ctx, traceLevel(elapsed, callErr), "finished", attrs...)
	}()
	waitLog := time.AfterFunc(slowCall, func() {
		trace.log(ctx, slog.LevelInfo, "slot_waiting", "elapsed", time.Since(call.since))
	})
	defer waitLog.Stop()
	select {
	case <-c.done:
		return ErrOffline
	case <-ctx.Done():
		return ctx.Err()
	case c.gate <- struct{}{}:
	}
	defer func() { <-c.gate }()
	waitLog.Stop()
	slotWait = time.Since(call.since)
	select {
	case <-c.done:
		return ErrOffline
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	tag = c.nextID.Add(1)
	trace.log(ctx, traceLevel(slotWait, nil), "slot_acquired", "tag", tag, "elapsed", slotWait)
	params.SetTag(tag)
	callCtx := ctx
	if onOutput != nil {
		callCtx, call.cancel = context.WithCancelCause(ctx)
		defer call.cancel(nil)
		call.ctx = callCtx
		call.chunks = make(chan OutputParams, maxOutputChunks)
	}
	defer c.trackCall(tag, call, onOutput)()

	var opts []jsonrpc2.CallOption
	if c.info.StartEvents {
		// Opt in per request so an older control gets the original message sequence.
		opts = append(opts, jsonrpc2.Meta(true))
	}
	waiter, err := c.conn.DispatchCall(callCtx, method, params, opts...)
	if err == nil {
		sent, sentAfter = true, time.Since(call.since)
		trace.log(ctx, slog.LevelDebug, "sent", "tag", tag, "elapsed", sentAfter)
		err = waiter.Wait(callCtx, result)
		if callCtx.Err() != nil && errors.Is(err, callCtx.Err()) {
			cancelSettlement = c.settleCancel(callCtx, waiter, tag)
			return context.Cause(callCtx)
		}
	}
	if errors.Is(err, jsonrpc2.ErrClosed) || errors.Is(err, errStreamWrite) {
		c.Close()
		return errors.Join(ErrUnknown, err)
	}
	// The cancel and final reply can both be ready. Report a full output queue
	// even if Wait got the reply first, since some output was not delivered.
	if errors.Is(context.Cause(callCtx), errOutputBackpressure) {
		return errOutputBackpressure
	}
	return machineFailure(err)
}

func (c *Client) settleCancel(ctx context.Context, waiter jsonrpc2.Waiter, tag uint64) string {
	// Keep the slot until the peer confirms the call ended. Notify can wait for
	// the JSON-RPC send lock, so the deadline must close the socket independently.
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), messageTimeout)
	defer cancel()
	stopClosing := context.AfterFunc(settle, c.Close)
	defer stopClosing()
	err := c.conn.Notify(settle, MethodCancel, CancelParams{Tag: tag})
	if err == nil {
		err = waiter.Wait(settle, nil)
	}
	switch {
	case settle.Err() != nil:
		c.Close()
		return "timeout"
	case errors.Is(err, jsonrpc2.ErrClosed), errors.Is(err, errStreamWrite):
		c.Close()
		return "disconnected"
	default:
		// The final reply, success or error, confirms the request is no longer running.
		return "acknowledged"
	}
}

type clientCall struct {
	ctx     context.Context
	since   time.Time
	trace   callTrace
	started atomic.Pointer[time.Time]
	cancel  context.CancelCauseFunc // non-nil only for calls that consume output
	chunks  chan OutputParams
	bytes   atomic.Int64
}

// trackCall keeps callbacks off the socket reader. A full buffer fails only
// the call that owns it instead of blocking unrelated replies and heartbeats.
func (c *Client) trackCall(tag uint64, call *clientCall, onOutput func(string, env.ShellStream)) func() {
	c.mu.Lock()
	c.calls[tag] = call
	c.mu.Unlock()
	var done chan struct{}
	if onOutput != nil {
		done = make(chan struct{})
		go func() {
			defer close(done)
			for chunk := range call.chunks {
				call.bytes.Add(-int64(len(chunk.Text)))
				if call.ctx.Err() == nil {
					onOutput(chunk.Text, env.ShellStream(chunk.Stream))
				}
			}
		}()
	}
	return func() {
		c.mu.Lock()
		delete(c.calls, tag)
		if done != nil {
			close(call.chunks)
		}
		c.mu.Unlock()
		// Preserve delivery order and never invoke a callback after Exec returns.
		if done != nil {
			<-done
		}
	}
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

// Exec runs one command and streams its output to onOutput. Callbacks for this
// call are serial and must return promptly; different calls can invoke callbacks
// concurrently. Exec waits for its last callback before returning.
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

func (h *clientHandler) Handle(ctx context.Context, _ *jsonrpc2.Conn, req *jsonrpc2.Request) {
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
	case MethodStarted:
		var id Identity
		if !req.Notif || decode(req.Params, &id) != nil {
			return
		}
		h.client.mu.Lock()
		call := h.client.calls[id.Tag]
		h.client.mu.Unlock()
		if call != nil && call.started.CompareAndSwap(nil, new(time.Now())) {
			call.trace.log(ctx, slog.LevelDebug, "remote_started", "tag", id.Tag, "elapsed", time.Since(call.since))
		}
	case MethodOutput:
		var output OutputParams
		if req.Params == nil || decode(req.Params, &output) != nil {
			return
		}
		h.client.mu.Lock()
		defer h.client.mu.Unlock()
		o := h.client.calls[output.Tag]
		if o == nil || o.chunks == nil || o.ctx.Err() != nil {
			return
		}
		size := int64(len(output.Text))
		if o.bytes.Add(size) <= maxOutputBytes {
			select {
			case o.chunks <- output:
				return
			default:
			}
		}
		o.bytes.Add(-size)
		o.cancel(errOutputBackpressure)
	}
}
