// Package worker runs the embedded Bun worker as a child process and talks to it over its stdin
// and stdout.
package worker

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// ErrUnavailable means that the worker process exited before it answered a call.
var ErrUnavailable = errors.New("worker exited")

// CallError preserves the worker's failure code at the protocol boundary.
type CallError struct {
	Method  string
	Code    string
	Message string
}

func (e *CallError) Error() string { return fmt.Sprintf("worker %s: %s", e.Method, e.Message) }

// Config configures a Worker.
type Config struct {
	Path string   // executable from Install
	Dir  string   // working directory of the process
	Env  []string // the complete environment; the process inherits nothing else
	// OnSettled receives each settled event, in order, on the goroutine that reads stdout.
	OnSettled func(context.Context, Settled)
	// OnTask runs outside the stdout reader, so it can call the worker.
	OnTask func(context.Context, TaskRequest) (any, error)
	// OnChat receives each chat event of a watch, in order, on the goroutine that reads stdout. It
	// must not block.
	OnChat func(ChatStream)
	// OnExit runs after each process exit. The watches of the process are gone.
	OnExit func()
	// Init runs after each process start, before Call can reach the process, for example to load
	// credentials. call sends a request to the new process.
	Init func(ctx context.Context, call CallFunc) error
	// OnStart runs on its own goroutine after Init, for example to replay submissions that the
	// previous process did not settle.
	OnStart func(ctx context.Context)
}

// CallFunc sends one request to the worker and decodes its result into result.
type CallFunc func(ctx context.Context, method string, params, result any) error

// Worker keeps one worker process running and restarts it when it exits.
type Worker struct {
	cfg Config

	mu   sync.Mutex
	proc *process      // nil while no process runs
	up   chan struct{} // closed while proc is not nil
	// restarting marks an exit that Restart requested, so Run starts the next process at once.
	restarting atomic.Bool
}

// New returns a Worker. Run starts it.
func New(cfg Config) *Worker {
	return &Worker{cfg: cfg, up: make(chan struct{})}
}

// Run starts the process and restarts it after each exit, until ctx is done. On cancel it closes
// the process's stdin, which makes the worker close its storage and exit.
func (w *Worker) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		started := time.Now()
		err := w.runOnce(ctx)
		if ctx.Err() != nil {
			return nil //nolint:nilerr // an exit after cancel is the requested stop
		}
		if w.restarting.Swap(false) {
			continue
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		slog.Error("worker exited; restarting", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (w *Worker) runOnce(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, w.cfg.Path) //nolint:gosec // Path comes from Install, not from input
	cmd.Dir = w.cfg.Dir
	cmd.Env = w.cfg.Env
	cmd.Stderr = os.Stderr
	childStdin, stdin, err := os.Pipe()
	if err != nil {
		return err
	}
	defer childStdin.Close()
	defer stdin.Close()
	cmd.Stdin = childStdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Cancel = stdin.Close
	cmd.WaitDelay = 10 * time.Second // then kill
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", w.cfg.Path, err)
	}

	_ = childStdin.Close()
	processCtx, cancelProcess := context.WithCancel(ctx)
	defer cancelProcess()
	p := &process{stdin: stdin, writer: make(chan struct{}, 1), pending: map[int64]chan incoming{}, done: make(chan struct{})}
	defer p.callbacks.Wait()
	readErr := make(chan error, 1)
	go func() {
		err := p.read(processCtx, stdout, w.cfg)
		cancelProcess()
		close(p.done)
		readErr <- err
	}()
	if w.cfg.Init != nil {
		if err := w.cfg.Init(processCtx, p.call); err != nil {
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			<-readErr
			return errors.Join(err, cmd.Wait())
		}
	}
	// When the process exited during Init, Call fails with ErrUnavailable until setProc(nil).
	w.setProc(p)
	if w.cfg.OnStart != nil {
		go w.cfg.OnStart(processCtx)
	}
	err = <-readErr
	w.setProc(nil)
	if w.cfg.OnExit != nil {
		w.cfg.OnExit()
	}
	return errors.Join(cmd.Wait(), err)
}

// Restart stops the process, which closes its storage, and returns when the next process accepts
// calls. The caller must stop new work first, for example with the maintenance lease.
func (w *Worker) Restart(ctx context.Context) error {
	w.mu.Lock()
	old := w.proc
	w.mu.Unlock()
	if old == nil {
		return ErrUnavailable
	}
	w.restarting.Store(true)
	_ = old.stdin.Close()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-old.done:
	}
	for {
		w.mu.Lock()
		p, up := w.proc, w.up
		w.mu.Unlock()
		if p != nil && p != old {
			return nil
		}
		if p == old {
			// Run has not seen the exit yet.
			time.Sleep(10 * time.Millisecond)
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-up:
		}
	}
}

func (w *Worker) setProc(p *process) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.proc = p
	if p != nil {
		close(w.up)
	} else {
		w.up = make(chan struct{})
	}
}

// Call sends one request and decodes its result into result. While no process runs, for example
// during a restart, it waits for one until ctx is done. It returns ErrUnavailable when the process
// exits before it answers.
func (w *Worker) Call(ctx context.Context, method string, params, result any) error {
	for {
		w.mu.Lock()
		p, up := w.proc, w.up
		w.mu.Unlock()
		if p != nil {
			return p.call(ctx, method, params, result)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-up:
		}
	}
}

// Bind returns a caller for the current process. It never waits for or switches to another process.
func (w *Worker) Bind() (CallFunc, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.proc == nil {
		return nil, ErrUnavailable
	}
	select {
	case <-w.proc.done:
		return nil, ErrUnavailable
	default:
		return w.proc.call, nil
	}
}

type process struct {
	stdin     *os.File
	writer    chan struct{}
	nextID    atomic.Int64
	callbacks sync.WaitGroup

	mu      sync.Mutex
	pending map[int64]chan incoming
	done    chan struct{} // closed after the process exits
}

func (p *process) call(ctx context.Context, method string, params, result any) error {
	id := p.nextID.Add(1)
	reply := make(chan incoming, 1)
	p.mu.Lock()
	p.pending[id] = reply
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}()

	line, err := json.Marshal(request{ID: id, Method: method, Params: params})
	if err != nil {
		return err
	}
	select {
	case p.writer <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return ErrUnavailable
	}
	err = p.write(ctx, append(line, '\n'))
	<-p.writer
	if err != nil {
		return errors.Join(ctx.Err(), fmt.Errorf("%w: %w", ErrUnavailable, err))
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return ErrUnavailable
	case msg := <-reply:
		if msg.Error != nil {
			return &CallError{Method: method, Code: msg.Error.Code, Message: msg.Error.Message}
		}
		return json.Unmarshal(msg.Result, result)
	}
}

// A partial JSON line cannot be reused. Close the pipe on write failure so the
// supervisor replaces the worker instead of sending more data after that line.
func (p *process) write(ctx context.Context, line []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.stdin.SetWriteDeadline(time.Time{}); err != nil {
		return err
	}
	cancelled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = p.stdin.SetWriteDeadline(time.Now()); close(cancelled) })
	_, err := p.stdin.Write(line)
	if !stop() {
		<-cancelled
	}
	_ = p.stdin.SetWriteDeadline(time.Time{})
	if err != nil {
		_ = p.stdin.Close()
	}
	return err
}

func (p *process) handleTask(processCtx context.Context, cfg Config, id int64, task TaskRequest) {
	ctx, cancel := context.WithTimeout(processCtx, 30*time.Second)
	defer cancel()
	var result any
	var err error
	if cfg.OnTask == nil {
		err = errors.New("task tools are unavailable")
	} else {
		result, err = cfg.OnTask(ctx, task)
	}
	reply := map[string]any{"id": id, "result": result}
	if err != nil {
		reply = map[string]any{"id": id, "error": map[string]string{"message": err.Error()}}
	}
	line, err := json.Marshal(reply)
	if err != nil {
		slog.Error("encode task response", "err", err)
		return
	}
	// Handler expiry must still reach the worker. Its process lifetime remains the outer limit.
	sendCtx, endSend := context.WithTimeout(processCtx, 5*time.Second)
	defer endSend()
	select {
	case p.writer <- struct{}{}:
	case <-sendCtx.Done():
		return
	case <-p.done:
		return
	}
	err = p.write(sendCtx, append(line, '\n'))
	<-p.writer
	if err != nil {
		slog.Warn("write task response", "err", err)
	}
}

func (p *process) read(ctx context.Context, stdout io.Reader, cfg Config) error {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(nil, 64<<20) // one line holds a whole answer
	for scanner.Scan() {
		var msg incoming
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			slog.Error("worker wrote a line that is not JSON", "err", err)
			continue
		}
		switch {
		case msg.Method == "task":
			var task TaskRequest
			if err := json.Unmarshal(msg.Params, &task); err != nil {
				return fmt.Errorf("bad task request: %w", err)
			}
			p.callbacks.Go(func() { p.handleTask(ctx, cfg, msg.ID, task) })
		case msg.Event == "settled":
			var settled Settled
			if err := json.Unmarshal(msg.Params, &settled); err != nil {
				slog.Error("bad settled event", "err", err)
				continue
			}
			if cfg.OnSettled != nil {
				cfg.OnSettled(ctx, settled)
			}
		case msg.Event == "chat":
			var stream ChatStream
			if err := json.Unmarshal(msg.Params, &stream); err != nil {
				slog.Error("bad chat event", "err", err)
				continue
			}
			if cfg.OnChat != nil {
				cfg.OnChat(stream)
			}
		case msg.Event != "":
			slog.Warn("unknown worker event", "event", msg.Event)
		default:
			p.mu.Lock()
			reply, ok := p.pending[msg.ID]
			p.mu.Unlock()
			if ok {
				reply <- msg
			}
		}
	}
	return scanner.Err()
}
