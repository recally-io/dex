package sysops

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/update"
)

var (
	// ErrInvalid reports an invalid request (for example an unknown update channel).
	ErrInvalid = errors.New("invalid system operation request")
	// ErrBusy reports that another upgrade or restart operation is already in progress.
	ErrBusy = errors.New("a system operation is already in progress")
	// ErrUnsupported reports that this build or host cannot execute the requested self-operation.
	ErrUnsupported = errors.New("operation is not supported on this build")
)

const (
	StateIdle        = "idle"
	StateChecking    = "checking"
	StateDownloading = "downloading"
	StateWaitingIdle = "waiting_idle"
	StateRestarting  = "restarting"
	StateError       = "error"

	ActionRestart = "restart"
	ActionUpgrade = "upgrade"

	ChannelStable     = "stable"
	ChannelPrerelease = "prerelease"
)

// Config configures a Manager.
type Config struct {
	DB                 *sql.DB
	DataDir            string
	Version            string
	StartedAt          time.Time
	Executable         string
	ReleaseBase        string
	Logs               *LogBuffer
	AcquireMaintenance func(ctx context.Context) error
	ReleaseMaintenance func(ctx context.Context) error
	RequestRestart     func(execPath string)
	IsBusyErr          func(error) bool
	WaitInterval       time.Duration
	WaitTimeout        time.Duration
	RestartDelay       time.Duration
}

// Status is a snapshot of server operations state for the UI and API.
type Status struct {
	Version         string
	StartedAt       int64
	State           string
	PendingAction   string
	TargetVersion   string
	DownloadDone    int64
	DownloadTotal   int64
	UpdateChannel   string
	Capability      CapabilityView
	Latest          *update.ReleaseInfo
	UpdateAvailable bool
	LastCheckedAt   int64
	LastError       string
	Backups         []BackupInfo
}

// CapabilityView reports whether self-upgrade and restart are available.
type CapabilityView struct {
	Supported  bool
	Reason     string
	CanRestart bool
}

// Manager coordinates server update checks, staged upgrades, restarts, backups, and log reads.
type Manager struct {
	cfg     Config
	q       *sqlc.Queries
	closeCh chan struct{}
	probe   update.Capability

	mu              sync.Mutex
	closed          bool
	op              *activeOp
	state           string
	pendingAction   string
	targetVersion   string
	downloadDone    int64
	downloadTotal   int64
	latest          *update.ReleaseInfo
	updateAvailable bool
	lastCheckedAt   int64
	lastError       string
	wg              sync.WaitGroup
}

type activeOp struct {
	cancel context.CancelFunc
	done   chan struct{}
	force  chan struct{}
}

// New creates a Manager for a running dex server.
func New(cfg Config) *Manager {
	if cfg.StartedAt.IsZero() {
		cfg.StartedAt = time.Now()
	}
	if cfg.Executable == "" {
		if exe, err := os.Executable(); err == nil {
			cfg.Executable = exe
		}
	}
	if cfg.WaitInterval <= 0 {
		cfg.WaitInterval = time.Second
	}
	if cfg.WaitTimeout <= 0 {
		cfg.WaitTimeout = 10 * time.Minute
	}
	if cfg.RestartDelay <= 0 {
		cfg.RestartDelay = 150 * time.Millisecond
	}
	var q *sqlc.Queries
	if cfg.DB != nil {
		q = sqlc.New(cfg.DB)
	}
	return &Manager{
		cfg:     cfg,
		q:       q,
		closeCh: make(chan struct{}),
		probe:   update.Probe(cfg.Version, cfg.Executable),
		state:   StateIdle,
	}
}

// StartedAt returns the server start time in Unix milliseconds.
func (m *Manager) StartedAt() int64 {
	if m == nil {
		return 0
	}
	return m.cfg.StartedAt.UnixMilli()
}

// Close cancels any background operation and waits for goroutines to finish cleanly.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	close(m.closeCh)
	if m.op != nil && m.state != StateRestarting {
		m.op.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// Status returns the current operations snapshot.
func (m *Manager) Status(ctx context.Context) (Status, error) {
	channel, err := m.channel(ctx)
	if err != nil {
		return Status{}, err
	}
	backups, err := ListBackups(m.cfg.DataDir)
	if err != nil {
		slog.Warn("list database backups", "err", err)
		backups = []BackupInfo{}
	}
	capInfo := m.capability()
	m.mu.Lock()
	defer m.mu.Unlock()
	var latestCopy *update.ReleaseInfo
	if m.latest != nil {
		latestCopy = new(*m.latest)
	}
	return Status{
		Version:         m.cfg.Version,
		StartedAt:       m.cfg.StartedAt.UnixMilli(),
		State:           m.state,
		PendingAction:   m.pendingAction,
		TargetVersion:   m.targetVersion,
		DownloadDone:    m.downloadDone,
		DownloadTotal:   m.downloadTotal,
		UpdateChannel:   channel,
		Capability:      capInfo,
		Latest:          latestCopy,
		UpdateAvailable: m.updateAvailable,
		LastCheckedAt:   m.lastCheckedAt,
		LastError:       m.lastError,
		Backups:         backups,
	}, nil
}

// SetChannel updates the persisted update channel (stable or prerelease).
func (m *Manager) SetChannel(ctx context.Context, channel string) (Status, error) {
	if channel != ChannelStable && channel != ChannelPrerelease {
		return Status{}, fmt.Errorf("%w: updateChannel must be stable or prerelease", ErrInvalid)
	}
	if m.q != nil {
		if err := m.q.SetUpdateChannel(ctx, channel); err != nil {
			return Status{}, err
		}
	}
	m.mu.Lock()
	if m.state == StateIdle || m.state == StateError {
		m.latest = nil
		m.updateAvailable = false
		m.lastCheckedAt = 0
		m.lastError = ""
		m.state = StateIdle
	}
	m.mu.Unlock()
	return m.Status(ctx)
}

// CheckUpdate queries GitHub Releases for the configured channel.
func (m *Manager) CheckUpdate(ctx context.Context) (Status, error) {
	channel, err := m.channel(ctx)
	if err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	if m.closed || m.state == StateChecking || m.state == StateDownloading || m.state == StateWaitingIdle || m.state == StateRestarting {
		m.mu.Unlock()
		return Status{}, ErrBusy
	}
	m.state = StateChecking
	m.lastError = ""
	m.mu.Unlock()

	u := update.NewForCheck(m.cfg.Version, m.cfg.ReleaseBase)
	info, available, err := u.CheckRelease(ctx, update.Options{Channel: channel})
	now := time.Now().UnixMilli()
	m.mu.Lock()
	if m.closed || m.state != StateChecking {
		m.mu.Unlock()
		return Status{}, ErrBusy
	}
	if err != nil {
		m.state = StateError
		m.lastError = err.Error()
		m.lastCheckedAt = now
		m.mu.Unlock()
		return Status{}, err
	}
	m.state = StateIdle
	m.latest = &info
	m.updateAvailable = available
	m.lastCheckedAt = now
	m.lastError = ""
	m.mu.Unlock()
	return m.Status(ctx)
}

// Backup creates a consistent hot backup of control.sqlite.
func (m *Manager) Backup(ctx context.Context) (BackupInfo, error) {
	return BackupDatabase(ctx, m.cfg.DB, m.cfg.DataDir, m.cfg.Version, time.Now())
}

// Logs returns recent structured log entries from the in-memory ring buffer.
func (m *Manager) Logs(limit int) []LogEntry {
	if limit <= 0 {
		limit = 200
	}
	if limit > defaultLogCapacity {
		limit = defaultLogCapacity
	}
	return m.cfg.Logs.Entries(limit)
}

// Restart restarts the dex server once idle, or immediately when force is true.
func (m *Manager) Restart(ctx context.Context, force bool) (Status, error) {
	if m.cfg.RequestRestart == nil {
		return Status{}, fmt.Errorf("%w: restart is unavailable in this process", ErrUnsupported)
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Status{}, ErrBusy
	}
	switch m.state {
	case StateRestarting:
		m.mu.Unlock()
		return m.Status(ctx)
	case StateChecking, StateDownloading:
		m.mu.Unlock()
		return Status{}, ErrBusy
	case StateWaitingIdle:
		if force && m.op != nil {
			select {
			case m.op.force <- struct{}{}:
			default:
			}
		}
		m.mu.Unlock()
		return m.Status(ctx)
	}
	m.pendingAction = ActionRestart
	m.lastError = ""
	if force || m.cfg.AcquireMaintenance == nil {
		m.state = StateRestarting
		m.mu.Unlock()
		m.scheduleRestart("")
		return m.Status(ctx)
	}
	m.state = StateWaitingIdle
	m.startOperation(ctx, func(ctx context.Context, op *activeOp) error {
		held, err := m.waitForMaintenance(ctx, op, ActionRestart, false)
		if err != nil {
			return err
		}
		if err := m.beginRestart(ctx); err != nil {
			m.releaseMaintenance(ctx, held)
			return err
		}
		slog.Info("restarting dex server")
		m.scheduleRestart("")
		return nil
	})
	m.mu.Unlock()
	return m.Status(ctx)
}

// Upgrade downloads, verifies, backs up control.sqlite, and restarts into targetTag (or channel latest).
func (m *Manager) Upgrade(ctx context.Context, targetTag string, force bool) (Status, error) {
	targetTag = strings.TrimSpace(targetTag)
	if m.cfg.RequestRestart == nil {
		return Status{}, fmt.Errorf("%w: restart is unavailable in this process", ErrUnsupported)
	}
	if !m.probe.Supported {
		return Status{}, fmt.Errorf("%w: self-upgrade unavailable (%s)", ErrUnsupported, m.probe.Reason)
	}
	channel, err := m.channel(ctx)
	if err != nil {
		return Status{}, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Status{}, ErrBusy
	}
	if m.state == StateWaitingIdle && m.pendingAction == ActionUpgrade && force && m.op != nil {
		select {
		case m.op.force <- struct{}{}:
		default:
		}
		m.mu.Unlock()
		return m.Status(ctx)
	}
	if m.state == StateChecking || m.state == StateDownloading || m.state == StateWaitingIdle || m.state == StateRestarting {
		m.mu.Unlock()
		return Status{}, ErrBusy
	}
	m.state = StateDownloading
	m.pendingAction = ActionUpgrade
	m.targetVersion = targetTag
	m.downloadDone = 0
	m.downloadTotal = 0
	m.lastError = ""
	m.startOperation(ctx, func(ctx context.Context, op *activeOp) error {
		return m.runUpgrade(ctx, op, m.probe.Executable, channel, targetTag, force)
	})
	m.mu.Unlock()
	return m.Status(ctx)
}

// startOperation starts the sole owner of the pending operation. The caller holds m.mu.
func (m *Manager) startOperation(ctx context.Context, run func(context.Context, *activeOp) error) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	op := &activeOp{cancel: cancel, done: make(chan struct{}), force: make(chan struct{}, 1)}
	m.op = op
	m.wg.Go(func() {
		defer cancel()
		defer close(op.done)
		err := run(ctx, op)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.op = nil
		if m.closed || (err == nil && m.state == StateRestarting) {
			return
		}
		m.pendingAction, m.targetVersion = "", ""
		m.downloadDone, m.downloadTotal = 0, 0
		if err == nil || errors.Is(err, context.Canceled) {
			m.state = StateIdle
			return
		}
		m.state = StateError
		m.lastError = err.Error()
		slog.Error("system operation failed", "err", err)
	})
}

// CancelPending cancels a pending waiting_idle or downloading operation.
func (m *Manager) CancelPending(ctx context.Context) (Status, error) {
	m.mu.Lock()
	if m.state == StateRestarting {
		m.mu.Unlock()
		return Status{}, ErrBusy
	}
	op := m.op
	if op != nil {
		op.cancel()
	}
	m.mu.Unlock()
	if op != nil {
		select {
		case <-op.done:
		case <-ctx.Done():
			return Status{}, ctx.Err()
		}
	}
	return m.Status(ctx)
}

func (m *Manager) runUpgrade(ctx context.Context, op *activeOp, executable, channel, targetTag string, force bool) error {
	u, err := update.NewWithBase(m.cfg.Version, executable, m.cfg.ReleaseBase)
	if err != nil {
		return err
	}
	opt := update.Options{Channel: channel}
	if targetTag != "" {
		opt = update.Options{Tag: targetTag}
	}
	stage, info, unlock, err := u.StageRelease(ctx, opt, func(done, total int64) {
		m.mu.Lock()
		if !m.closed {
			m.downloadDone = done
			m.downloadTotal = total
		}
		m.mu.Unlock()
	})
	if err != nil {
		return cmp.Or(ctx.Err(), err)
	}
	// Ownership of stage and unlock belongs strictly to this goroutine; release them on every exit.
	defer unlock()
	defer func() { _ = os.Remove(stage) }()

	// Back up control.sqlite before touching the running binary or acquiring maintenance.
	if _, err := m.Backup(ctx); err != nil {
		return cmp.Or(ctx.Err(), err)
	}

	m.mu.Lock()
	if m.closed || ctx.Err() != nil {
		m.mu.Unlock()
		return context.Canceled
	}
	m.targetVersion = info.Tag
	m.latest = &info
	m.updateAvailable = true
	m.mu.Unlock()

	held, err := m.waitForMaintenance(ctx, op, ActionUpgrade, force)
	if err != nil {
		return err
	}
	if err := m.beginRestart(ctx); err != nil {
		m.releaseMaintenance(ctx, held)
		return err
	}
	if err := u.Replace(stage); err != nil {
		m.releaseMaintenance(ctx, held)
		return err
	}
	slog.Info("staged dex upgrade applied; restarting server", "target", info.Tag)
	m.scheduleRestart(executable)
	return nil
}

// waitForMaintenance reports whether this operation acquired the lease.
func (m *Manager) waitForMaintenance(ctx context.Context, op *activeOp, action string, force bool) (bool, error) {
	if force || m.cfg.AcquireMaintenance == nil {
		return false, nil
	}
	if err := m.cfg.AcquireMaintenance(ctx); err == nil {
		return true, nil
	} else if !m.isBusy(err) {
		return false, err
	}
	m.mu.Lock()
	if m.closed || ctx.Err() != nil {
		m.mu.Unlock()
		return false, context.Canceled
	}
	m.state = StateWaitingIdle
	m.mu.Unlock()

	deadline := time.NewTimer(m.cfg.WaitTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(m.cfg.WaitInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-op.force:
			return false, nil
		case <-deadline.C:
			return false, fmt.Errorf("timed out waiting for active runs to finish before %s", action)
		case <-ticker.C:
			if err := m.cfg.AcquireMaintenance(ctx); err == nil {
				return true, nil
			} else if !m.isBusy(err) {
				return false, err
			}
		}
	}
}

func (m *Manager) releaseMaintenance(ctx context.Context, held bool) {
	if held && m.cfg.ReleaseMaintenance != nil {
		_ = m.cfg.ReleaseMaintenance(context.WithoutCancel(ctx))
	}
}

// beginRestart closes the cancellation window before an executable can be replaced.
func (m *Manager) beginRestart(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || ctx.Err() != nil {
		return context.Canceled
	}
	m.state = StateRestarting
	return nil
}

func (m *Manager) scheduleRestart(execPath string) {
	delay := m.cfg.RestartDelay
	cb := m.cfg.RequestRestart
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.wg.Go(func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-m.closeCh:
			return
		case <-timer.C:
			cb(execPath)
		}
	})
	m.mu.Unlock()
}

func (m *Manager) channel(ctx context.Context) (string, error) {
	if m.q == nil {
		return ChannelStable, nil
	}
	row, err := m.q.GetSystemSettings(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return ChannelStable, nil
	}
	if err != nil {
		return "", err
	}
	return cmp.Or(row.UpdateChannel, ChannelStable), nil
}

func (m *Manager) capability() CapabilityView {
	return CapabilityView{
		Supported:  m.probe.Supported,
		Reason:     m.probe.Reason,
		CanRestart: m.cfg.RequestRestart != nil,
	}
}

func (m *Manager) isBusy(err error) bool {
	if m.cfg.IsBusyErr != nil {
		return m.cfg.IsBusyErr(err)
	}
	return false
}
