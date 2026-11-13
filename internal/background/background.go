// Package background runs shell commands for sessions and wakes the owning session when a
// command ends or prints a line.
//
// A background command is a child process of dex in its own process group. Output goes to a file
// under the agent data directory. The state lives in control.sqlite. A wake-up is a durable input
// of the owning session, like the result of a child session.
//
// A command owns its whole process group: when the leader ends, dex ends the rest of the group.
// dex stops every running command when it shuts down. Only a crash leaves a command that dex
// cannot account for, and the next start marks it unknown.
package background

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// Wake modes.
const (
	WakeExit  = "exit"
	WakeLines = "lines"
)

// States of a background command.
const (
	// StateStarting is the state of a row before its process exists. A crash can leave it.
	StateStarting = "starting"
	StateRunning  = "running"
	StateExited   = "exited"
	StateStopped  = "stopped"
	StateTimedOut = "timed_out"
	// StateUnknown is the state of a command that a crash of dex left in starting or running. The
	// process can still run: dex cannot tell.
	StateUnknown = "unknown"
)

// Reasons why dex ends a command.
const (
	reasonStop        = "stop"
	reasonTimeout     = "timeout"
	reasonOutputLimit = "output_limit"
	reasonRateLimit   = "rate_limit"
	// reasonShutdown is the end that dex causes when it closes, so a restart never leaves a
	// process that no tool can stop.
	reasonShutdown = "shutdown"
	// reasonStartFailed is the end of a command that dex started and could not track.
	reasonStartFailed = "start_failed"
)

var (
	// ErrInvalid is a request that no state change can fix.
	ErrInvalid = errors.New("invalid background command")
	// ErrLimit means that the bot runs the most background commands that it may run.
	ErrLimit = errors.New("too many background commands")
	// ErrNotFound means that the session has no background command with the ID.
	ErrNotFound = errors.New("background command not found")
)

// Limits bound the cost of background commands. A zero field takes its default.
type Limits struct {
	// MaxRunning is the most running commands of one bot.
	MaxRunning int
	// OutputBytes is the size of the output file at which dex stops the command. It is a soft limit:
	// dex checks the size every 250 ms, so a fast writer can pass it before the stop.
	OutputBytes int64
	// LinesPerWindow is the most stdout lines that a lines command may print in one Window.
	LinesPerWindow int
	Window         time.Duration
	// Batch is how long dex collects lines before it wakes the session once.
	Batch time.Duration
	// Grace is the time between SIGTERM and SIGKILL.
	Grace time.Duration
	// ExitTimeout and LinesTimeout are the default timeouts. MaxLinesTimeout is the lines maximum.
	ExitTimeout, LinesTimeout, MaxLinesTimeout time.Duration
}

func (l Limits) withDefaults() Limits {
	l.MaxRunning = cmp.Or(l.MaxRunning, 8)
	l.OutputBytes = cmp.Or(l.OutputBytes, 64<<20)
	l.LinesPerWindow = cmp.Or(l.LinesPerWindow, 100)
	l.Window = cmp.Or(l.Window, time.Minute)
	l.Batch = cmp.Or(l.Batch, time.Second)
	l.Grace = cmp.Or(l.Grace, 5*time.Second)
	l.ExitTimeout = cmp.Or(l.ExitTimeout, 30*time.Minute)
	l.LinesTimeout = cmp.Or(l.LinesTimeout, 5*time.Minute)
	l.MaxLinesTimeout = cmp.Or(l.MaxLinesTimeout, 30*time.Minute)
	return l
}

const (
	// A wake-up carries at most this much of the output. The output file has the rest.
	tailLines  = 20
	tailBytes  = 4 << 10
	batchLines = 50
	batchBytes = 8 << 10
	// A line is cut here. The rest of it is in the output file.
	longestLine       = 64 << 10
	storeAttempts     = 5
	storeRetryBackoff = 100 * time.Millisecond
	sizePoll          = 250 * time.Millisecond
)

// StartParams describe one command.
type StartParams struct {
	// CallKey identifies the tool call. A second start with the same key returns the first command.
	CallKey         string
	AgentID         string
	ConversationID  int64
	SourceRequestID string
	Command         string
	Wake            string
	// Timeout is zero for the default of the wake mode.
	Timeout time.Duration
	// Cwd is empty or relative to DefaultCwd.
	Cwd        string
	DefaultCwd string
	Label      string
	Env        map[string]string
	// OutputDir holds the output file.
	OutputDir string
}

// Manager owns the processes of background commands.
type Manager struct {
	db     *sql.DB
	q      *sqlc.Queries
	now    func() time.Time
	limits Limits
	// changed runs after a wake-up input is stored: the owner admits it.
	changed func(conversationID int64)

	wg sync.WaitGroup // supervisors and delayed kills

	mu     sync.Mutex // starts, so the cap and the call key checks are atomic
	closed bool
	procs  map[string]*proc
}

// New returns a manager. changed runs after a wake-up is stored and may be nil.
func New(db *sql.DB, now func() time.Time, limits Limits, changed func(conversationID int64)) *Manager {
	return &Manager{db: db, q: sqlc.New(db), now: now, limits: limits.withDefaults(), changed: changed, procs: make(map[string]*proc)}
}

// Recover marks every starting and running command unknown. Call it once at start, before any
// command starts. dex stops its commands when it shuts down, so such a row means a crash: the
// process may still run, and dex does not say that it ended.
func (m *Manager) Recover(ctx context.Context) error {
	_, err := m.q.MarkBackgroundCommandsUnknown(ctx)
	return err
}

// Close ends every running command and waits until each final state and end wake-up is stored:
// SIGTERM to the process group, and SIGKILL after the grace period. Call it before the database
// closes. A start after Close fails.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	running := make([]*proc, 0, len(m.procs))
	for _, p := range m.procs {
		running = append(running, p)
	}
	m.mu.Unlock()
	for _, p := range running {
		m.terminate(p, reasonShutdown)
	}
	m.wg.Wait()
}

type proc struct {
	row  sqlc.BackgroundCommand
	cmd  *exec.Cmd
	pipe *os.File // read end of stdout, for a lines command

	exited chan struct{} // closed when the leader is reaped
	done   chan struct{} // closed when the final state is stored

	// mu guards the fields below. sweeping is set once supervise owns the group: from then on terminate
	// sends no signal, so a late signal cannot reach a group that dex already ended.
	mu          sync.Mutex
	reason      string
	terminating bool
	sweeping    bool
	termAt      time.Time // when terminate sent the first SIGTERM

	// Lines state. eventMu orders wake-ups.
	eventMu      sync.Mutex
	pending      []string
	pendingBytes int
	omitted      int
	batchTimer   *time.Timer
	sent         int
	windowAt     time.Time
	windowN      int
}

// Start runs a command in the background and returns its row. The row exists before the process:
// a crash between the two leaves a row in state starting, and a replay of the same call finds it
// and never starts a second process.
func (m *Manager) Start(ctx context.Context, p StartParams) (sqlc.BackgroundCommand, error) {
	if strings.TrimSpace(p.Command) == "" {
		return sqlc.BackgroundCommand{}, fmt.Errorf("%w: the command is empty", ErrInvalid)
	}
	p.Wake = cmp.Or(p.Wake, WakeExit)
	if p.Wake != WakeExit && p.Wake != WakeLines {
		return sqlc.BackgroundCommand{}, fmt.Errorf("%w: wake is exit or lines", ErrInvalid)
	}
	if p.Timeout < 0 {
		return sqlc.BackgroundCommand{}, fmt.Errorf("%w: the timeout is negative", ErrInvalid)
	}
	if p.Wake == WakeLines {
		p.Timeout = cmp.Or(p.Timeout, m.limits.LinesTimeout)
		if p.Timeout > m.limits.MaxLinesTimeout {
			return sqlc.BackgroundCommand{}, fmt.Errorf("%w: a lines command runs at most %d seconds; start it again after it ends", ErrInvalid, int(m.limits.MaxLinesTimeout.Seconds()))
		}
	} else {
		p.Timeout = cmp.Or(p.Timeout, m.limits.ExitTimeout)
	}
	cwd := p.Cwd
	if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(p.DefaultCwd, cwd)
	}
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return sqlc.BackgroundCommand{}, fmt.Errorf("%w: %s is not a directory", ErrInvalid, cwd)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return sqlc.BackgroundCommand{}, fmt.Errorf("%w: dex is shutting down", ErrInvalid)
	}
	if row, err := m.q.GetBackgroundCommandByKey(ctx, p.CallKey); err == nil {
		return row, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return sqlc.BackgroundCommand{}, err
	}
	running, err := m.q.CountRunningBackgroundCommands(ctx, p.AgentID)
	if err != nil {
		return sqlc.BackgroundCommand{}, err
	}
	if int(running) >= m.limits.MaxRunning {
		return sqlc.BackgroundCommand{}, fmt.Errorf("%w: this bot runs %d; stop one or wait for one to end", ErrLimit, running)
	}

	id := uuid.NewV7().String()
	if err := os.MkdirAll(p.OutputDir, 0o700); err != nil {
		return sqlc.BackgroundCommand{}, err
	}
	output := filepath.Join(p.OutputDir, id+".log")
	label := cmp.Or(strings.TrimSpace(p.Label), cut(p.Command, 60))
	timeoutMs := sql.NullInt64{}
	if p.Timeout > 0 {
		timeoutMs = sql.NullInt64{Int64: p.Timeout.Milliseconds(), Valid: true}
	}
	created, err := m.q.CreateBackgroundCommand(ctx, sqlc.CreateBackgroundCommandParams{
		ID: id, CallKey: p.CallKey, ConversationID: p.ConversationID, SourceRequestID: p.SourceRequestID, Wake: p.Wake,
		Label: label, Command: p.Command, Cwd: cwd, OutputPath: output, TimeoutMs: timeoutMs, ProcessStartedAt: m.now().UnixMilli(),
	})
	if err != nil {
		return sqlc.BackgroundCommand{}, err
	}
	if created == 0 {
		return sqlc.BackgroundCommand{}, fmt.Errorf("%w: the call key is in use", ErrInvalid)
	}
	started, err := m.launch(ctx, p, cwd, output)
	if err != nil {
		// The command never ran, so no row stays and a replayed call can try again.
		if derr := m.q.DeleteBackgroundCommand(context.WithoutCancel(ctx), id); derr != nil {
			slog.Error("delete background command that did not start", "id", id, "err", derr)
		}
		return sqlc.BackgroundCommand{}, err
	}
	// The process runs. From here the row stays, so the call key stays used and a replay never
	// starts a second process, and the writes ignore the cancel of the tool call.
	dbctx := context.WithoutCancel(ctx)
	row, err := m.markRunning(dbctx, id, started)
	if err != nil {
		m.abandon(dbctx, started, id)
		return sqlc.BackgroundCommand{}, err
	}
	started.row = row
	m.procs[id] = started
	// supervise outlives the tool call that started the command, so it takes no context from it.
	m.wg.Go(func() { m.supervise(started, p.Timeout) }) //nolint:contextcheck // lifetime is the manager's, not the call's
	return row, nil
}

func (m *Manager) markRunning(ctx context.Context, id string, started *proc) (sqlc.BackgroundCommand, error) {
	pid := int64(started.cmd.Process.Pid)
	if _, err := m.q.MarkBackgroundCommandRunning(ctx, sqlc.MarkBackgroundCommandRunningParams{ID: id, Pid: pid, Pgid: pid, StartedAt: m.now().UnixMilli()}); err != nil {
		return sqlc.BackgroundCommand{}, err
	}
	return m.q.GetBackgroundCommand(ctx, id)
}

// abandon ends a process that no supervisor follows, because the row could not be stored as running.
// The row stays with the state stopped, so the replay of the call finds it.
func (m *Manager) abandon(ctx context.Context, started *proc, id string) {
	signal(started, syscall.SIGKILL)
	<-started.exited
	if started.pipe != nil {
		_ = started.pipe.Close()
	}
	_, err := m.q.FinishBackgroundCommand(ctx, sqlc.FinishBackgroundCommandParams{
		ID: id, State: StateStopped, ExitStatus: sql.NullInt64{Int64: int64(exitStatus(started.cmd.ProcessState)), Valid: true}, Reason: reasonStartFailed, EndedAt: sql.NullInt64{Int64: m.now().UnixMilli(), Valid: true},
	})
	if err != nil {
		// The row stays in state starting, and the next start of dex marks it unknown.
		slog.Error("store background command that lost its supervisor", "id", id, "err", err)
	}
}

// launch starts the process. A lines command writes stdout to a pipe that dex reads. Every other
// stream goes straight to the output file, so the process does not depend on dex for its output.
func (m *Manager) launch(ctx context.Context, p StartParams, cwd, output string) (*proc, error) {
	file, err := os.OpenFile(output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // The path is under the agent data directory.
	if err != nil {
		return nil, err
	}
	defer file.Close()
	shell := "sh"
	if path, err := exec.LookPath("bash"); err == nil {
		shell = path
	}
	// The process outlives the start call, so the context must not cancel it.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), shell, "-c", p.Command) //nolint:gosec // Running the command of the model is the purpose of the tool.
	cmd.Dir = cwd
	cmd.Env = make([]string, 0, len(p.Env))
	for k, v := range p.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// A new session makes the shell the leader of its own process group, so a stop reaches
	// everything that the command started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stderr = file
	started := &proc{cmd: cmd, exited: make(chan struct{}), done: make(chan struct{})}
	var write *os.File
	if p.Wake == WakeLines {
		if started.pipe, write, err = os.Pipe(); err != nil {
			return nil, err
		}
		cmd.Stdout = write
	} else {
		cmd.Stdout = file
	}
	err = cmd.Start()
	if write != nil {
		_ = write.Close()
	}
	if err != nil {
		if started.pipe != nil {
			_ = started.pipe.Close()
		}
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	go func() {
		_ = cmd.Wait()
		close(started.exited)
	}()
	return started, nil
}

// supervise follows one process to its end and stores the final state.
func (m *Manager) supervise(p *proc, timeout time.Duration) {
	var timer *time.Timer
	if timeout > 0 {
		timer = time.AfterFunc(timeout, func() { m.terminate(p, reasonTimeout) })
	}
	pumped := make(chan struct{})
	if p.pipe != nil {
		go func() { defer close(pumped); m.pump(p) }()
	} else {
		close(pumped)
	}
	poll := time.NewTicker(sizePoll)
	defer poll.Stop()
wait:
	for {
		select {
		case <-p.exited:
			break wait
		case <-poll.C:
			if info, err := os.Stat(p.row.OutputPath); err == nil && info.Size() > m.limits.OutputBytes {
				m.terminate(p, reasonOutputLimit)
			}
		}
	}
	if timer != nil {
		timer.Stop()
	}
	// The command owns its group. Members that outlive the leader end now, so no process stays
	// that no tool can stop. After this the group is gone and supervise sends no more signals.
	m.sweep(p)
	if p.pipe != nil {
		// A process that left the group can hold the pipe open. The pipe holds what the command wrote
		// before it ended, so a short deadline loses nothing.
		_ = p.pipe.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		<-pumped
		_ = p.pipe.Close()
	}
	p.mu.Lock()
	reason := p.reason
	p.mu.Unlock()
	state, status := StateExited, exitStatus(p.cmd.ProcessState)
	switch reason {
	case "":
	case reasonTimeout:
		state = StateTimedOut
	default:
		state = StateStopped
	}
	// A stop by the session or the user needs no wake-up: they know.
	if reason != reasonStop {
		p.flush(m)
	}
	m.finish(p, state, status, reason)
	m.mu.Lock()
	delete(m.procs, p.row.ID)
	m.mu.Unlock()
	close(p.done)
}

func exitStatus(state *os.ProcessState) int {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return state.ExitCode()
}

// storeEnd writes the final state and the end wake-up in one transaction. It reports whether it
// stored a wake-up.
func (m *Manager) storeEnd(ctx context.Context, p *proc, state string, status int, reason string, wake *sqlc.CreateBackgroundWakeParams) (bool, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	// Take the writer before any read, as the chat service does, so that this transaction cannot
	// fail on an upgrade of its lock.
	if _, err := tx.ExecContext(ctx, "UPDATE conversations SET id = id WHERE 0"); err != nil {
		return false, err
	}
	q := m.q.WithTx(tx)
	changed, err := q.FinishBackgroundCommand(ctx, sqlc.FinishBackgroundCommandParams{
		ID: p.row.ID, State: state, ExitStatus: sql.NullInt64{Int64: int64(status), Valid: true}, Reason: reason, EndedAt: sql.NullInt64{Int64: m.now().UnixMilli(), Valid: true},
	})
	if err != nil || changed == 0 || wake == nil {
		return false, errors.Join(err, tx.Commit())
	}
	stored, err := q.CreateBackgroundWake(ctx, *wake)
	if err != nil {
		return false, err
	}
	return stored > 0, tx.Commit()
}

// finish stores the final state and the end wake-up in one transaction, so a crash cannot leave
// an ended row without its wake-up.
func (m *Manager) finish(p *proc, state string, status int, reason string) {
	ctx := context.Background()
	var wake *sqlc.CreateBackgroundWakeParams
	if reason != reasonStop {
		params, err := m.wakeParams(p, "end", endBody(p.row, state, status, reason, m.limits))
		if err != nil {
			slog.Error("encode background end wake-up", "id", p.row.ID, "err", err)
		} else {
			wake = &params
		}
	}
	// After the last attempt the row stays running until the next start marks it unknown.
	var woke bool
	err := retryStore("store background command end", p.row.ID, func() (err error) {
		woke, err = m.storeEnd(ctx, p, state, status, reason, wake)
		return err
	})
	if err != nil {
		slog.Error("store background command end", "id", p.row.ID, "err", err)
		return
	}
	if woke && m.changed != nil {
		m.changed(p.row.ConversationID)
	}
}

// sweep ends the process group after its leader exited: SIGTERM, then SIGKILL after the grace
// period. A group ID stays in use while a member runs, so a signal cannot reach an unrelated
// process until the last member is gone. sweep stops signalling when the group is empty.
//
// The leader is reaped, so its PID can be reused once the group is empty. A signal that arrives in
// that window could hit another group. dex accepts it: closing it needs delayed reaping.
func (m *Manager) sweep(p *proc) {
	p.mu.Lock()
	p.sweeping = true
	termAt := p.termAt
	p.mu.Unlock()
	if !groupAlive(p) {
		return
	}
	if termAt.IsZero() {
		signal(p, syscall.SIGTERM)
		termAt = time.Now()
	}
	// A stop that sent SIGTERM before the leader ended has used part of the grace already.
	if waitGroupGone(p, time.Until(termAt.Add(m.limits.Grace))) {
		return
	}
	signal(p, syscall.SIGKILL)
	if !waitGroupGone(p, time.Second) {
		slog.Warn("background process group survives SIGKILL", "id", p.row.ID, "pgid", p.cmd.Process.Pid)
	}
}

func groupAlive(p *proc) bool {
	return !errors.Is(syscall.Kill(-p.cmd.Process.Pid, 0), syscall.ESRCH)
}

func waitGroupGone(p *proc, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for groupAlive(p) {
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// terminate sends SIGTERM to the process group, and SIGKILL after the grace period unless supervise
// took the group first: once the leader ended, supervise sweeps the group and terminate sends no
// signal. The first reason wins, until the final state is stored.
func (m *Manager) terminate(p *proc, reason string) {
	select {
	case <-p.done:
		return
	default:
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reason == "" {
		p.reason = reason
	}
	if p.terminating || p.sweeping {
		return
	}
	p.terminating = true
	p.termAt = time.Now()
	signal(p, syscall.SIGTERM)
	m.wg.Go(func() {
		select {
		case <-p.exited:
		case <-time.After(m.limits.Grace):
			p.mu.Lock()
			defer p.mu.Unlock()
			if !p.sweeping {
				signal(p, syscall.SIGKILL)
			}
		}
	})
}

func signal(p *proc, sig syscall.Signal) {
	_ = syscall.Kill(-p.cmd.Process.Pid, sig)
}

// Stop ends the process group of one command of a session and waits until the state is stored.
func (m *Manager) Stop(ctx context.Context, conversationID int64, id string) (sqlc.BackgroundCommand, error) {
	row, err := m.owned(ctx, conversationID, id)
	if err != nil {
		return row, err
	}
	switch row.State {
	case StateStarting, StateRunning:
	case StateUnknown:
		// The process can be gone, and the OS can have given its PID to another process.
		return row, fmt.Errorf("%w: dex crashed and cannot confirm which process is this command; stop it by hand if it still runs (PID %d)", ErrInvalid, row.Pid)
	default:
		return row, nil
	}
	// A start holds the lock until its process has a supervisor, so a command in state starting
	// is registered here.
	m.mu.Lock()
	p := m.procs[id]
	m.mu.Unlock()
	if p == nil {
		return m.owned(ctx, conversationID, id)
	}
	m.terminate(p, reasonStop)
	select {
	case <-p.done:
	case <-ctx.Done():
		return row, ctx.Err()
	}
	return m.owned(ctx, conversationID, id)
}

// StopConversation ends the process group of each running command of a session. It does not wait.
func (m *Manager) StopConversation(ctx context.Context, conversationID int64) error {
	ids, err := m.q.ListRunningBackgroundIDs(ctx, conversationID)
	if err != nil {
		return err
	}
	m.stopAll(ids)
	return nil
}

// StopAgent ends the process group of each running command of a bot.
func (m *Manager) StopAgent(ctx context.Context, agentID string) error {
	ids, err := m.q.ListRunningBackgroundIDsByAgent(ctx, agentID)
	if err != nil {
		return err
	}
	m.stopAll(ids)
	return nil
}

func (m *Manager) stopAll(ids []string) {
	// Start holds the lock while it starts a process, so every listed command is registered here.
	m.mu.Lock()
	var targets []*proc
	for _, id := range ids {
		if p := m.procs[id]; p != nil {
			targets = append(targets, p)
		}
	}
	m.mu.Unlock()
	for _, p := range targets {
		m.terminate(p, reasonStop)
	}
}

// List returns the commands of a session, the newest first.
func (m *Manager) List(ctx context.Context, conversationID int64) ([]sqlc.BackgroundCommand, error) {
	return m.q.ListBackgroundCommands(ctx, conversationID)
}

func (m *Manager) owned(ctx context.Context, conversationID int64, id string) (sqlc.BackgroundCommand, error) {
	row, err := m.q.GetBackgroundCommand(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.ConversationID != conversationID {
		return row, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return row, err
}

// pump reads the stdout of a lines command. It copies every byte to the output file and turns
// each line into an event. It keeps reading after an error, so the process never blocks on a full pipe.
func (m *Manager) pump(p *proc) {
	file, err := os.OpenFile(p.row.OutputPath, os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // The path is under the agent data directory.
	if err != nil {
		slog.Error("open background output", "id", p.row.ID, "err", err)
		file = nil
	} else {
		defer file.Close()
	}
	lines := lineSplitter{emit: func(line string) { m.event(p, line) }}
	buf := make([]byte, 32<<10)
	writeFailed := false
	for {
		n, err := p.pipe.Read(buf)
		if n > 0 {
			if file != nil && !writeFailed {
				if _, werr := file.Write(buf[:n]); werr != nil {
					writeFailed = true
					slog.Error("write background output", "id", p.row.ID, "err", werr)
				}
			}
			lines.feed(buf[:n])
		}
		if err != nil {
			// The end of the stream, a deadline after the leader ended, and a closed pipe are normal ends.
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrDeadlineExceeded) && !errors.Is(err, os.ErrClosed) {
				slog.Error("read background output", "id", p.row.ID, "err", err)
			}
			lines.end()
			return
		}
	}
}

// lineSplitter turns a byte stream into lines. A line over longestLine is cut, and the rest of it
// is dropped up to the next newline: later lines still arrive.
type lineSplitter struct {
	line []byte
	emit func(string)
}

func (s *lineSplitter) feed(data []byte) {
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		segment := data
		if i >= 0 {
			segment = data[:i]
		}
		segment = segment[:min(len(segment), longestLine-len(s.line))]
		s.line = append(s.line, segment...)
		if i < 0 {
			return
		}
		s.flushLine()
		data = data[i+1:]
	}
}

// end emits a last line that has no newline.
func (s *lineSplitter) end() {
	if len(s.line) > 0 {
		s.flushLine()
	}
}

func (s *lineSplitter) flushLine() {
	text := strings.ToValidUTF8(strings.TrimSuffix(string(s.line), "\r"), "")
	s.line = s.line[:0]
	s.emit(text)
}

// event adds one line to the batch. Too many lines in one window stop the command: a flood
// would fill the context of the session.
func (m *Manager) event(p *proc, line string) {
	p.eventMu.Lock()
	defer p.eventMu.Unlock()
	now := m.now()
	if now.Sub(p.windowAt) >= m.limits.Window {
		p.windowAt, p.windowN = now, 0
	}
	p.windowN++
	if p.windowN > m.limits.LinesPerWindow {
		p.mu.Lock()
		limited := p.reason == reasonRateLimit
		p.mu.Unlock()
		if !limited {
			m.terminate(p, reasonRateLimit)
		}
		return
	}
	line = cut(line, batchBytes/4)
	// A line over either limit of the batch is counted, and the output file has it.
	if len(p.pending) >= batchLines || p.pendingBytes+len(line) > batchBytes {
		p.omitted++
		return
	}
	p.pending = append(p.pending, line)
	p.pendingBytes += len(line)
	if p.batchTimer == nil {
		p.batchTimer = time.AfterFunc(m.limits.Batch, func() { p.flush(m) })
	}
}

// flush wakes the session with the lines that wait. A write that fails after all attempts loses
// that batch: the output file still has the lines, and the end wake-up carries the tail of it.
// The attempts hold eventMu, so a busy database slows the reader of the pipe for a few seconds.
func (p *proc) flush(m *Manager) {
	p.eventMu.Lock()
	defer p.eventMu.Unlock()
	if p.batchTimer != nil {
		p.batchTimer.Stop()
		p.batchTimer = nil
	}
	if len(p.pending) == 0 {
		return
	}
	p.mu.Lock()
	stopped := p.reason == reasonStop
	p.mu.Unlock()
	lines, omitted := p.pending, p.omitted
	p.pending, p.pendingBytes, p.omitted = nil, 0, 0
	if stopped {
		return
	}
	p.sent++
	params, err := m.wakeParams(p, fmt.Sprint(p.sent), result{ID: p.row.ID, Label: p.row.Label, Event: "lines", Lines: lines, Omitted: omitted, OutputFile: p.row.OutputPath})
	var stored int64
	if err == nil {
		err = retryStore("store background lines wake-up", p.row.ID, func() (err error) {
			stored, err = m.q.CreateBackgroundWake(context.Background(), params)
			return err
		})
	}
	if err != nil {
		slog.Error("store background lines wake-up", "id", p.row.ID, "err", err)
		return
	}
	if stored > 0 && m.changed != nil {
		m.changed(p.row.ConversationID)
	}
}

// retryStore runs a write a few times with backoff. A busy database is the credible failure: under
// load a writer can hold the lock past the busy timeout. The wake-up ID makes a repeated insert a
// no-op, so a write that committed before an error is not stored twice.
func retryStore(what, id string, write func() error) error {
	var err error
	for attempt, backoff := 0, storeRetryBackoff; attempt < storeAttempts; attempt, backoff = attempt+1, backoff*2 {
		if attempt > 0 {
			time.Sleep(backoff)
		}
		if err = write(); err == nil {
			return nil
		}
		slog.Warn(what, "id", id, "attempt", attempt+1, "err", err)
	}
	return err
}

// result is the body of a wake-up. The key "background-result" tells the model, and the
// transcript, what kind of input this is.
type result struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Event is lines, exited, timed_out, or stopped.
	Event      string   `json:"event"`
	ExitStatus *int     `json:"exitStatus,omitempty"`
	Notice     string   `json:"notice,omitempty"`
	Lines      []string `json:"lines,omitempty"`
	Omitted    int      `json:"omitted,omitzero"`
	Output     string   `json:"output,omitempty"`
	OutputFile string   `json:"outputFile"`
}

func endBody(row sqlc.BackgroundCommand, state string, status int, reason string, limits Limits) result {
	body := result{ID: row.ID, Label: row.Label, Event: state, ExitStatus: &status, OutputFile: row.OutputPath}
	switch reason {
	case reasonTimeout:
		body.Notice = fmt.Sprintf("dex ended the command after its timeout of %d seconds.", row.TimeoutMs.Int64/1000)
	case reasonOutputLimit:
		body.Notice = fmt.Sprintf("dex stopped the command because its output passed %d bytes. The limit is soft, so the file can be larger.", limits.OutputBytes)
	case reasonRateLimit:
		body.Notice = fmt.Sprintf("dex stopped the command because it printed more than %d lines in %s. Later lines were not sent. Read the output file, and print less.", limits.LinesPerWindow, limits.Window)
	case reasonShutdown:
		body.Notice = "dex stopped the command because dex shut down. Start it again if the work is not finished."
	}
	body.Output = tail(row.OutputPath)
	return body
}

// wakeParams encodes a wake-up as a pending input of the owning session. The insert stores
// nothing for an archived bot, which takes no input.
func (m *Manager) wakeParams(p *proc, suffix string, body result) (sqlc.CreateBackgroundWakeParams, error) {
	content, err := json.Marshal(struct {
		Result result `json:"background-result"`
	}{body})
	if err != nil {
		return sqlc.CreateBackgroundWakeParams{}, err
	}
	return sqlc.CreateBackgroundWakeParams{
		ID: "background:" + p.row.ID + ":" + suffix, Content: string(content), CreatedAt: m.now().UnixMilli(), BackgroundID: p.row.ID,
	}, nil
}

// tail returns the last lines of the output file, bounded by lines and bytes.
func tail(path string) string {
	file, err := os.Open(path) //nolint:gosec // The path is the output file of a command.
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	start := max(info.Size()-tailBytes, 0)
	data := make([]byte, info.Size()-start)
	if _, err := file.ReadAt(data, start); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	text := strings.TrimRight(string(data), "\n")
	if start > 0 {
		// The first line is cut in the middle.
		if _, rest, ok := strings.Cut(text, "\n"); ok {
			text = rest
		}
	}
	lines := strings.Split(text, "\n")
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	return strings.Join(lines, "\n")
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "..."
}
