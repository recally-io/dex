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
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"uuid"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/execenv"
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
	stateRunning  = "running"
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
	// errLimit means that the bot runs the most background commands that it may run.
	errLimit = errors.New("too many background commands")
	// errNotFound means that the session has no background command with the ID.
	errNotFound = errors.New("background command not found")
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
	InheritEnv bool
	EnvKind    execenv.Kind
	// OutputDir holds the output file.
	OutputDir string
}

// ExecEnvOpener acquires an execution environment for a non-local background command.
type ExecEnvOpener func(ctx context.Context, agentID, cwd string, extraEnv map[string]string) (durableenv.ExecutionEnv, string, execenv.Kind, func(), error)

// outputHooks receive live command output chunks and events from a runner.
type outputHooks struct {
	OnStdoutLine func(line string)
	OnSizeLimit  func()
}

// processResult is the outcome and bounded tail output of a completed background process.
type processResult struct {
	ExitCode int
	Tail     string
}

// runningProcess controls one launched background command.
type runningProcess interface {
	PID() int64
	Wait() processResult
	Terminate(grace time.Duration)
	Abandon()
}

// runner starts and supervises background commands inside a local or remote execution environment.
type runner interface {
	ValidateDir(ctx context.Context, p StartParams, cwd string) error
	PrepareOutput(ctx context.Context, p StartParams, id string) (string, error)
	Start(ctx context.Context, p StartParams, id, cwd, outputPath string, hooks outputHooks) (runningProcess, error)
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

	mu        sync.Mutex // starts, so the cap and the call key checks are atomic
	closed    bool
	procs     map[string]*proc
	envOpener ExecEnvOpener
}

// New returns a manager. changed runs after a wake-up is stored and may be nil.
func New(db *sql.DB, now func() time.Time, limits Limits, changed func(conversationID int64)) *Manager {
	return &Manager{db: db, q: sqlc.New(db), now: now, limits: limits.withDefaults(), changed: changed, procs: make(map[string]*proc)}
}

// ConfigureEnvOpener installs the execution environment opener used for non-local agents.
func (m *Manager) ConfigureEnvOpener(opener ExecEnvOpener) {
	m.mu.Lock()
	m.envOpener = opener
	m.mu.Unlock()
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
	run  runningProcess
	done chan struct{} // closed when the final state is stored

	// mu guards reason so the first termination reason wins until the final state is stored.
	mu     sync.Mutex
	reason string

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

func (m *Manager) runnerFor(kind execenv.Kind, opener ExecEnvOpener) (runner, error) {
	if kind == "" || kind == execenv.KindLocal {
		return &localRunner{limits: m.limits, wg: &m.wg}, nil
	}
	if opener == nil {
		return nil, fmt.Errorf("%w: remote background runner is unavailable", ErrInvalid)
	}
	return newEnvRunner(opener, m.limits), nil
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
	if !execenv.IsAbsPath(cwd) {
		cwd = execenv.JoinTargetPath(p.DefaultCwd, cwd)
	}

	m.mu.Lock()
	opener := m.envOpener
	m.mu.Unlock()
	runner, err := m.runnerFor(p.EnvKind, opener)
	if err != nil {
		return sqlc.BackgroundCommand{}, err
	}
	if err := runner.ValidateDir(ctx, p, cwd); err != nil {
		return sqlc.BackgroundCommand{}, err
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
		return sqlc.BackgroundCommand{}, fmt.Errorf("%w: this bot runs %d; stop one or wait for one to end", errLimit, running)
	}

	id := uuid.NewV7().String()
	output, err := runner.PrepareOutput(ctx, p, id)
	if err != nil {
		return sqlc.BackgroundCommand{}, err
	}
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
	started, err := m.launch(ctx, runner, id, p, cwd, output)
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
	pid := started.run.PID()
	if _, err := m.q.MarkBackgroundCommandRunning(ctx, sqlc.MarkBackgroundCommandRunningParams{ID: id, Pid: pid, Pgid: pid, StartedAt: m.now().UnixMilli()}); err != nil {
		return sqlc.BackgroundCommand{}, err
	}
	return m.q.GetBackgroundCommand(ctx, id)
}

// abandon ends a process that no supervisor follows, because the row could not be stored as running.
// The row stays with the state stopped, so the replay of the call finds it.
func (m *Manager) abandon(ctx context.Context, started *proc, id string) {
	started.run.Abandon()
	res := started.run.Wait()
	_, err := m.q.FinishBackgroundCommand(ctx, sqlc.FinishBackgroundCommandParams{
		ID: id, State: StateStopped, ExitStatus: sql.NullInt64{Int64: int64(res.ExitCode), Valid: true}, Reason: reasonStartFailed, EndedAt: sql.NullInt64{Int64: m.now().UnixMilli(), Valid: true},
	})
	if err != nil {
		// The row stays in state starting, and the next start of dex marks it unknown.
		slog.Error("store background command that lost its supervisor", "id", id, "err", err)
	}
}

func (m *Manager) launch(ctx context.Context, r runner, id string, p StartParams, cwd, output string) (*proc, error) {
	started := &proc{done: make(chan struct{})}
	run, err := r.Start(ctx, p, id, cwd, output, outputHooks{
		OnStdoutLine: func(line string) { m.event(started, line) }, //nolint:contextcheck // line batching outlives the start tool call
		OnSizeLimit:  func() { m.terminate(started, reasonOutputLimit) },
	})
	if err != nil {
		return nil, err
	}
	started.run = run
	return started, nil
}

// supervise follows one process to its end and stores the final state.
func (m *Manager) supervise(p *proc, timeout time.Duration) {
	var timer *time.Timer
	if timeout > 0 {
		timer = time.AfterFunc(timeout, func() { m.terminate(p, reasonTimeout) })
	}
	res := p.run.Wait()
	if timer != nil {
		timer.Stop()
	}
	p.mu.Lock()
	reason := p.reason
	p.mu.Unlock()
	state := StateExited
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
	m.finish(p, state, res.ExitCode, reason, res.Tail)
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
func (m *Manager) finish(p *proc, state string, status int, reason, tailText string) {
	ctx := context.Background()
	var wake *sqlc.CreateBackgroundWakeParams
	if reason != reasonStop {
		params, err := m.wakeParams(p, "end", endBody(p.row, state, status, reason, m.limits, tailText))
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

// terminate requests shutdown of a running command. The first reason wins until the final state is stored.
func (m *Manager) terminate(p *proc, reason string) {
	select {
	case <-p.done:
		return
	default:
	}
	p.mu.Lock()
	if p.reason == "" {
		p.reason = reason
	}
	p.mu.Unlock()
	p.run.Terminate(m.limits.Grace)
}

// Stop ends the process group of one command of a session and waits until the state is stored.
func (m *Manager) Stop(ctx context.Context, conversationID int64, id string) (sqlc.BackgroundCommand, error) {
	row, err := m.owned(ctx, conversationID, id)
	if err != nil {
		return row, err
	}
	switch row.State {
	case StateStarting, stateRunning:
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
		return row, fmt.Errorf("%w: %s", errNotFound, id)
	}
	return row, err
}

// localRunner spawns OS child processes directly on the control host.
type localRunner struct {
	limits Limits
	wg     *sync.WaitGroup
}

func (r *localRunner) ValidateDir(_ context.Context, _ StartParams, cwd string) error {
	if info, err := os.Stat(cwd); err != nil || !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrInvalid, cwd)
	}
	return nil
}

func (r *localRunner) PrepareOutput(_ context.Context, p StartParams, id string) (string, error) {
	if err := os.MkdirAll(p.OutputDir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(p.OutputDir, id+".log"), nil
}

func (r *localRunner) Start(ctx context.Context, p StartParams, id, cwd, output string, hooks outputHooks) (runningProcess, error) {
	file, err := os.OpenFile(output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // The path is under the agent data directory.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	shell := "sh"
	if shellPath, err := exec.LookPath("bash"); err == nil {
		shell = shellPath
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
	lp := &localProc{
		id:         id,
		outputPath: output,
		cmd:        cmd,
		limits:     r.limits,
		wg:         r.wg,
		hooks:      hooks,
		exited:     make(chan struct{}),
	}
	var write *os.File
	if p.Wake == WakeLines {
		if lp.pipe, write, err = os.Pipe(); err != nil {
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
		if lp.pipe != nil {
			_ = lp.pipe.Close()
		}
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	go func() {
		_ = cmd.Wait()
		close(lp.exited)
	}()
	return lp, nil
}

type localProc struct {
	id         string
	outputPath string
	cmd        *exec.Cmd
	pipe       *os.File
	limits     Limits
	wg         *sync.WaitGroup
	hooks      outputHooks
	exited     chan struct{}

	mu          sync.Mutex
	terminating bool
	sweeping    bool
	termAt      time.Time
}

func (p *localProc) PID() int64 {
	return int64(p.cmd.Process.Pid)
}

func (p *localProc) Wait() processResult {
	pumped := make(chan struct{})
	if p.pipe != nil {
		go func() { defer close(pumped); p.pump() }()
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
			if info, err := os.Stat(p.outputPath); err == nil && info.Size() > p.limits.OutputBytes {
				if p.hooks.OnSizeLimit != nil {
					p.hooks.OnSizeLimit()
				}
			}
		}
	}
	// The command owns its group. Members that outlive the leader end now, so no process stays
	// that no tool can stop. After this the group is gone and Terminate sends no more signals.
	p.sweep()
	if p.pipe != nil {
		// A process that left the group can hold the pipe open. The pipe holds what the command wrote
		// before it ended, so a short deadline loses nothing.
		_ = p.pipe.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		<-pumped
		_ = p.pipe.Close()
	}
	return processResult{
		ExitCode: exitStatus(p.cmd.ProcessState),
		Tail:     tail(p.outputPath),
	}
}

func (p *localProc) Terminate(grace time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.terminating || p.sweeping {
		return
	}
	p.terminating = true
	p.termAt = time.Now()
	p.signal(syscall.SIGTERM)
	p.wg.Go(func() {
		select {
		case <-p.exited:
		case <-time.After(grace):
			p.mu.Lock()
			defer p.mu.Unlock()
			if !p.sweeping {
				p.signal(syscall.SIGKILL)
			}
		}
	})
}

func (p *localProc) Abandon() {
	p.signal(syscall.SIGKILL)
}

func (p *localProc) signal(sig syscall.Signal) {
	_ = syscall.Kill(-p.cmd.Process.Pid, sig)
}

func (p *localProc) groupAlive() bool {
	return !errors.Is(syscall.Kill(-p.cmd.Process.Pid, 0), syscall.ESRCH)
}

func (p *localProc) sweep() {
	p.mu.Lock()
	p.sweeping = true
	termAt := p.termAt
	p.mu.Unlock()
	if !p.groupAlive() {
		return
	}
	if termAt.IsZero() {
		p.signal(syscall.SIGTERM)
		termAt = time.Now()
	}
	if p.waitGroupGone(time.Until(termAt.Add(p.limits.Grace))) {
		return
	}
	p.signal(syscall.SIGKILL)
	if !p.waitGroupGone(time.Second) {
		slog.Warn("background process group survives SIGKILL", "id", p.id, "pgid", p.cmd.Process.Pid)
	}
}

func (p *localProc) waitGroupGone(limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for p.groupAlive() {
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// pump reads the stdout of a lines command. It copies every byte to the output file and turns
// each line into an event. It keeps reading after an error, so the process never blocks on a full pipe.
func (p *localProc) pump() {
	file, err := os.OpenFile(p.outputPath, os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // The path is under the agent data directory.
	if err != nil {
		slog.Error("open background output", "id", p.id, "err", err)
		file = nil
	} else {
		defer func() { _ = file.Close() }()
	}
	lines := lineSplitter{emit: p.hooks.OnStdoutLine}
	buf := make([]byte, 32<<10)
	writeFailed := false
	for {
		n, err := p.pipe.Read(buf)
		if n > 0 {
			if file != nil && !writeFailed {
				if _, werr := file.Write(buf[:n]); werr != nil {
					writeFailed = true
					slog.Error("write background output", "id", p.id, "err", werr)
				}
			}
			lines.feed(buf[:n])
		}
		if err != nil {
			// The end of the stream, a deadline after the leader ended, and a closed pipe are normal ends.
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrDeadlineExceeded) && !errors.Is(err, os.ErrClosed) {
				slog.Error("read background output", "id", p.id, "err", err)
			}
			lines.end()
			return
		}
	}
}

// envRunner executes background commands over an agent's durableenv.ExecutionEnv.
type envRunner struct {
	openEnv ExecEnvOpener
	limits  Limits
}

// newEnvRunner creates a runner that executes commands via durableenv.ExecutionEnv.
func newEnvRunner(openEnv ExecEnvOpener, limits Limits) *envRunner {
	return &envRunner{openEnv: openEnv, limits: limits}
}

func (r *envRunner) ValidateDir(ctx context.Context, p StartParams, cwd string) error {
	execEnv, _, _, release, err := r.openEnv(ctx, p.AgentID, cwd, p.Env)
	if err != nil {
		return err
	}
	if release != nil {
		defer release()
	}
	info, err := execenv.StatFollow(ctx, execEnv, cwd)
	if err != nil || info.Kind != durableenv.FileKindDirectory {
		return fmt.Errorf("%w: %s is not a directory", ErrInvalid, cwd)
	}
	return nil
}

func (r *envRunner) PrepareOutput(ctx context.Context, p StartParams, id string) (string, error) {
	execEnv, workspace, _, release, err := r.openEnv(ctx, p.AgentID, p.DefaultCwd, p.Env)
	if err != nil {
		return "", err
	}
	if release != nil {
		defer release()
	}
	outDir := cmp.Or(p.OutputDir, execenv.JoinTargetPath(workspace, ".dex", "background"))
	if err := execEnv.CreateDir(ctx, outDir, &durableenv.CreateDirOptions{Recursive: new(true)}); err != nil {
		return "", err
	}
	outputPath := execenv.JoinTargetPath(outDir, id+".log")
	if err := execEnv.WriteFile(ctx, outputPath, ""); err != nil {
		return "", err
	}
	return outputPath, nil
}

func (r *envRunner) Start(ctx context.Context, p StartParams, id, cwd, outputPath string, hooks outputHooks) (runningProcess, error) {
	baseCtx := context.WithoutCancel(ctx)
	execEnv, _, _, release, err := r.openEnv(baseCtx, p.AgentID, cwd, p.Env)
	if err != nil {
		return nil, err
	}
	execCtx, cancel := context.WithCancel(baseCtx) //nolint:gosec // cancel is stored on ep and invoked in Terminate/Abandon; execution intentionally outlives Start call
	ep := &envProc{
		id:         id,
		execEnv:    execEnv,
		cwd:        cwd,
		outputPath: outputPath,
		command:    p.Command,
		env:        maps.Clone(p.Env),
		inheritEnv: p.InheritEnv,
		wake:       p.Wake,
		limits:     r.limits,
		hooks:      hooks,
		cancel:     cancel,
		release:    release,
		ready:      make(chan struct{}),
		done:       make(chan struct{}),
		lines:      lineSplitter{emit: hooks.OnStdoutLine},
	}
	go ep.run(execCtx)
	return ep, nil
}

type envProc struct {
	id         string
	execEnv    durableenv.ExecutionEnv
	cwd        string
	outputPath string
	command    string
	env        map[string]string
	inheritEnv bool
	wake       string
	limits     Limits
	hooks      outputHooks
	cancel     context.CancelFunc
	release    func()
	readyOnce  sync.Once
	ready      chan struct{}
	done       chan struct{}

	outMu        sync.Mutex
	totalBytes   int64
	tailFallback []byte
	lines        lineSplitter

	result processResult
}

func (p *envProc) PID() int64 { return 0 }

func (p *envProc) activate() {
	p.readyOnce.Do(func() { close(p.ready) })
}

func (p *envProc) run(execCtx context.Context) {
	defer close(p.done)
	defer p.cancel()
	defer func() {
		if p.release != nil {
			p.release()
		}
	}()
	<-p.ready
	if execCtx.Err() != nil {
		p.result = processResult{ExitCode: 143}
		return
	}
	script := `( eval "$1" ) 2>&1 | tee -a "$2"; exit "${PIPESTATUS[0]}"`
	if p.wake == WakeLines {
		script = `( eval "$1" ) 2>>"$2" | tee -a "$2"; exit "${PIPESTATUS[0]}"`
	}
	res, err := p.execEnv.Exec(execCtx, []string{"/bin/bash", "-c", script, "dex-bg", p.command, p.outputPath}, &durableenv.ShellExecOptions{
		Cwd:        p.cwd,
		Env:        p.env,
		InheritEnv: new(p.inheritEnv),
		OnOutput:   p.onOutput,
	})
	p.outMu.Lock()
	if p.wake == WakeLines {
		p.lines.end()
	}
	total := p.totalBytes
	fallback := bytes.Clone(p.tailFallback)
	p.outMu.Unlock()

	status := res.ExitCode
	if err != nil {
		if ee, ok := errors.AsType[*durableenv.ExecutionError](err); ok && ee.Code == durableenv.ExecutionErrorAborted {
			status = 143
		} else {
			if status == 0 {
				status = 1
			}
			if total == 0 {
				fallback = append(fallback, (err.Error() + "\n")...)
			}
		}
	}
	var tailText string
	if len(fallback) > 0 {
		tailText = formatTailBytes(fallback, total > int64(len(fallback)))
	}
	p.result = processResult{ExitCode: status, Tail: tailText}
}

func (p *envProc) onOutput(_ context.Context, text string, info durableenv.ShellOutputInfo) {
	if text == "" {
		return
	}
	p.outMu.Lock()
	defer p.outMu.Unlock()
	p.totalBytes += int64(len(text))
	p.tailFallback = append(p.tailFallback, text...)
	maxKeep := max(tailBytes*2, 8192)
	if len(p.tailFallback) > maxKeep {
		p.tailFallback = bytes.Clone(p.tailFallback[len(p.tailFallback)-maxKeep:])
	}
	if p.limits.OutputBytes > 0 && p.totalBytes > p.limits.OutputBytes && p.hooks.OnSizeLimit != nil {
		p.hooks.OnSizeLimit()
	}
	if p.wake == WakeLines && info.Stream == durableenv.ShellStdout {
		p.lines.feed([]byte(text))
	}
}

func (p *envProc) Wait() processResult {
	p.activate()
	<-p.done
	return p.result
}

func (p *envProc) Terminate(_ time.Duration) {
	p.cancel()
}

func (p *envProc) Abandon() {
	p.cancel()
	p.activate()
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

func endBody(row sqlc.BackgroundCommand, state string, status int, reason string, limits Limits, tailText string) result {
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
	body.Output = tailText
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
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	start := max(info.Size()-tailBytes, 0)
	data := make([]byte, info.Size()-start)
	if _, err := file.ReadAt(data, start); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	return formatTailBytes(data, start > 0)
}

func formatTailBytes(data []byte, trimmedStart bool) string {
	text := strings.TrimRight(string(data), "\n")
	if trimmedStart {
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
