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

// Config configures a Worker.
type Config struct {
	Path string   // executable from Install
	Dir  string   // working directory of the process
	Env  []string // the complete environment; the process inherits nothing else
	// OnSettled receives each settled event, in order, on the goroutine that reads stdout.
	OnSettled func(context.Context, Settled)
	// OnChat receives each chat event of a watch, in order, on the goroutine that reads stdout. It
	// must not block.
	OnChat func(ChatStream)
	// OnExit runs after each process exit. The watches of the process are gone.
	OnExit func()
	// Init runs after each process start, before Call can reach the process, for example to load
	// credentials. call sends a request to the new process.
	Init func(ctx context.Context, call CallFunc)
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
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Cancel = stdin.Close
	cmd.WaitDelay = 10 * time.Second // then kill
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", w.cfg.Path, err)
	}

	p := &process{stdin: stdin, pending: map[int64]chan incoming{}, done: make(chan struct{})}
	readErr := make(chan error, 1)
	go func() {
		err := p.read(ctx, stdout, w.cfg)
		close(p.done)
		readErr <- err
	}()
	if w.cfg.Init != nil {
		w.cfg.Init(ctx, p.call)
	}
	// When the process exited during Init, Call fails with ErrUnavailable until setProc(nil).
	w.setProc(p)
	if w.cfg.OnStart != nil {
		go w.cfg.OnStart(ctx)
	}
	err = <-readErr
	w.setProc(nil)
	if w.cfg.OnExit != nil {
		w.cfg.OnExit()
	}
	return errors.Join(cmd.Wait(), err)
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

type process struct {
	stdin   io.Writer
	writeMu sync.Mutex
	nextID  atomic.Int64

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
	p.writeMu.Lock()
	_, err = p.stdin.Write(append(line, '\n'))
	p.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return ErrUnavailable
	case msg := <-reply:
		if msg.Error != nil {
			return fmt.Errorf("worker %s: %s", method, msg.Error.Message)
		}
		return json.Unmarshal(msg.Result, result)
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
