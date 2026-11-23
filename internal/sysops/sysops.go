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

	mu              sync.Mutex
	closed          bool
	state           string
	pendingAction   string
	targetVersion   string
	stagedPath      string
	stagedUnlock    func()
	downloadDone    int64
	downloadTotal   int64
	latest          *update.ReleaseInfo
	updateAvailable bool
	lastCheckedAt   int64
	lastError       string
	opCancel        context.CancelFunc
	wg              sync.WaitGroup
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

// Close cancels any background operation and cleans up staged files.
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
	cancel := m.opCancel
	m.opCancel = nil
	m.cleanupStagedLocked()
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
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
	if m.state == StateDownloading || m.state == StateWaitingIdle || m.state == StateRestarting {
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
	if m.state == StateRestarting {
		m.mu.Unlock()
		return m.Status(ctx)
	}
	if m.state == StateDownloading {
		m.mu.Unlock()
		return Status{}, ErrBusy
	}
	if m.state == StateWaitingIdle {
		if !force {
			m.mu.Unlock()
			return m.Status(ctx)
		}
		if m.pendingAction == ActionUpgrade {
			// Escalate a waiting upgrade to immediate replacement and restart.
			cancel := m.opCancel
			m.opCancel = nil
			m.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			return m.finishStagedUpgradeNow(ctx)
		}
		cancel := m.opCancel
		m.opCancel = nil
		m.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return m.triggerRestartNow(ctx, "")
	}
	m.mu.Unlock()

	if force || m.cfg.AcquireMaintenance == nil {
		return m.triggerRestartNow(ctx, "")
	}
	if err := m.cfg.AcquireMaintenance(ctx); err != nil {
		if m.isBusy(err) {
			m.startWaitingRestart(ctx)
			return m.Status(ctx)
		}
		return Status{}, err
	}
	return m.triggerRestartNow(ctx, "")
}

// Upgrade downloads, verifies, backs up control.sqlite, and restarts into targetTag (or channel latest).
func (m *Manager) Upgrade(ctx context.Context, targetTag string, force bool) (Status, error) {
	targetTag = strings.TrimSpace(targetTag)
	if m.cfg.RequestRestart == nil {
		return Status{}, fmt.Errorf("%w: restart is unavailable in this process", ErrUnsupported)
	}
	probe := update.Probe(m.cfg.Version, m.cfg.Executable)
	if !probe.Supported {
		return Status{}, fmt.Errorf("%w: self-upgrade unavailable (%s)", ErrUnsupported, probe.Reason)
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
	if m.state == StateWaitingIdle && m.pendingAction == ActionUpgrade && force {
		cancel := m.opCancel
		m.opCancel = nil
		m.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return m.finishStagedUpgradeNow(ctx)
	}
	if m.state == StateDownloading || m.state == StateWaitingIdle || m.state == StateRestarting {
		m.mu.Unlock()
		return Status{}, ErrBusy
	}
	opCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	m.opCancel = cancel
	m.state = StateDownloading
	m.pendingAction = ActionUpgrade
	m.targetVersion = targetTag
	m.downloadDone = 0
	m.downloadTotal = 0
	m.lastError = ""
	m.mu.Unlock()

	m.wg.Go(func() {
		m.runUpgrade(opCtx, probe.Executable, channel, targetTag, force)
	})
	return m.Status(ctx)
}

// CancelPending cancels a pending waiting_idle restart or upgrade and removes any staged binary.
func (m *Manager) CancelPending(ctx context.Context) (Status, error) {
	m.mu.Lock()
	if m.state == StateRestarting {
		m.mu.Unlock()
		return Status{}, ErrBusy
	}
	cancel := m.opCancel
	m.opCancel = nil
	wasWaitingOrDownloading := m.state == StateWaitingIdle || m.state == StateDownloading
	if wasWaitingOrDownloading {
		m.cleanupStagedLocked()
		m.state = StateIdle
		m.pendingAction = ""
		m.targetVersion = ""
		m.downloadDone = 0
		m.downloadTotal = 0
		m.lastError = ""
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return m.Status(ctx)
}

func (m *Manager) runUpgrade(ctx context.Context, executable, channel, targetTag string, force bool) {
	u, err := update.NewWithBase(m.cfg.Version, executable, m.cfg.ReleaseBase)
	if err != nil {
		m.failOperation(err)
		return
	}
	opt := update.Options{Channel: channel}
	if targetTag != "" {
		opt = update.Options{Tag: targetTag}
	}
	stage, info, unlock, err := u.StageRelease(ctx, opt, func(done, total int64) {
		m.mu.Lock()
		if m.state == StateDownloading {
			m.downloadDone = done
			m.downloadTotal = total
		}
		m.mu.Unlock()
	})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		m.failOperation(err)
		return
	}
	// Back up control.sqlite before touching the running binary or requesting maintenance.
	if _, err := m.Backup(ctx); err != nil {
		unlock()
		_ = os.Remove(stage)
		if ctx.Err() != nil {
			return
		}
		m.failOperation(err)
		return
	}

	m.mu.Lock()
	if m.closed || ctx.Err() != nil {
		m.mu.Unlock()
		unlock()
		_ = os.Remove(stage)
		return
	}
	m.stagedPath = stage
	m.stagedUnlock = unlock
	m.targetVersion = info.Tag
	m.latest = &info
	m.updateAvailable = true
	m.mu.Unlock()

	if !force && m.cfg.AcquireMaintenance != nil {
		if err := m.cfg.AcquireMaintenance(ctx); err != nil {
			if m.isBusy(err) {
				m.waitAndFinishUpgrade(ctx)
				return
			}
			m.failOperation(err)
			return
		}
	}
	if err := m.applyStagedAndRestart(u); err != nil {
		if m.cfg.ReleaseMaintenance != nil {
			_ = m.cfg.ReleaseMaintenance(context.WithoutCancel(ctx))
		}
		m.failOperation(err)
	}
}

func (m *Manager) waitAndFinishUpgrade(ctx context.Context) {
	m.mu.Lock()
	if m.closed || ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	m.state = StateWaitingIdle
	m.pendingAction = ActionUpgrade
	m.mu.Unlock()

	deadline := time.NewTimer(m.cfg.WaitTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(m.cfg.WaitInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			m.failOperation(errors.New("timed out waiting for active runs to finish before upgrading"))
			return
		case <-ticker.C:
			if err := m.cfg.AcquireMaintenance(ctx); err != nil {
				if m.isBusy(err) {
					continue
				}
				if ctx.Err() != nil {
					return
				}
				m.failOperation(err)
				return
			}
			probe := update.Probe(m.cfg.Version, m.cfg.Executable)
			u, err := update.NewWithBase(m.cfg.Version, probe.Executable, m.cfg.ReleaseBase)
			if err != nil {
				if m.cfg.ReleaseMaintenance != nil {
					_ = m.cfg.ReleaseMaintenance(context.WithoutCancel(ctx))
				}
				m.failOperation(err)
				return
			}
			if err := m.applyStagedAndRestart(u); err != nil {
				if m.cfg.ReleaseMaintenance != nil {
					_ = m.cfg.ReleaseMaintenance(context.WithoutCancel(ctx))
				}
				m.failOperation(err)
			}
			return
		}
	}
}

func (m *Manager) finishStagedUpgradeNow(ctx context.Context) (Status, error) {
	probe := update.Probe(m.cfg.Version, m.cfg.Executable)
	u, err := update.NewWithBase(m.cfg.Version, probe.Executable, m.cfg.ReleaseBase)
	if err != nil {
		m.failOperation(err)
		return Status{}, err
	}
	if err := m.applyStagedAndRestart(u); err != nil {
		m.failOperation(err)
		return Status{}, err
	}
	return m.Status(ctx)
}

func (m *Manager) applyStagedAndRestart(u *update.Updater) error {
	m.mu.Lock()
	stage := m.stagedPath
	unlock := m.stagedUnlock
	m.stagedPath = ""
	m.stagedUnlock = nil
	if stage == "" {
		m.mu.Unlock()
		if unlock != nil {
			unlock()
		}
		return errors.New("no staged upgrade binary is available")
	}
	if err := u.Replace(stage); err != nil {
		m.mu.Unlock()
		_ = os.Remove(stage)
		if unlock != nil {
			unlock()
		}
		return err
	}
	if unlock != nil {
		unlock()
	}
	m.state = StateRestarting
	m.pendingAction = ActionUpgrade
	m.opCancel = nil
	m.lastError = ""
	execPath := m.cfg.Executable
	m.mu.Unlock()

	slog.Info("staged dex upgrade applied; restarting server", "target", m.targetVersion)
	m.scheduleRestart(execPath)
	return nil
}

func (m *Manager) startWaitingRestart(ctx context.Context) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	opCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	m.opCancel = cancel
	m.state = StateWaitingIdle
	m.pendingAction = ActionRestart
	m.lastError = ""
	m.mu.Unlock()

	m.wg.Go(func() {
		deadline := time.NewTimer(m.cfg.WaitTimeout)
		defer deadline.Stop()
		ticker := time.NewTicker(m.cfg.WaitInterval)
		defer ticker.Stop()

		for {
			select {
			case <-opCtx.Done():
				return
			case <-deadline.C:
				m.failOperation(errors.New("timed out waiting for active runs to finish before restarting"))
				return
			case <-ticker.C:
				if err := m.cfg.AcquireMaintenance(opCtx); err != nil {
					if m.isBusy(err) {
						continue
					}
					if opCtx.Err() != nil {
						return
					}
					m.failOperation(err)
					return
				}
				m.mu.Lock()
				if m.closed || opCtx.Err() != nil {
					m.mu.Unlock()
					if m.cfg.ReleaseMaintenance != nil {
						_ = m.cfg.ReleaseMaintenance(context.WithoutCancel(opCtx))
					}
					return
				}
				m.state = StateRestarting
				m.pendingAction = ActionRestart
				m.opCancel = nil
				m.mu.Unlock()
				slog.Info("server is idle; restarting")
				m.scheduleRestart("")
				return
			}
		}
	})
}

func (m *Manager) triggerRestartNow(ctx context.Context, execPath string) (Status, error) {
	m.mu.Lock()
	m.state = StateRestarting
	m.pendingAction = ActionRestart
	m.lastError = ""
	m.mu.Unlock()
	slog.Info("restarting dex server")
	m.scheduleRestart(execPath)
	return m.Status(ctx)
}

func (m *Manager) scheduleRestart(execPath string) {
	delay := m.cfg.RestartDelay
	cb := m.cfg.RequestRestart
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
}

func (m *Manager) failOperation(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupStagedLocked()
	if m.closed {
		return
	}
	m.state = StateError
	m.pendingAction = ""
	m.opCancel = nil
	m.lastError = err.Error()
	slog.Error("system operation failed", "err", err)
}

func (m *Manager) cleanupStagedLocked() {
	if m.stagedPath != "" {
		_ = os.Remove(m.stagedPath)
		m.stagedPath = ""
	}
	if m.stagedUnlock != nil {
		m.stagedUnlock()
		m.stagedUnlock = nil
	}
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
	probe := update.Probe(m.cfg.Version, m.cfg.Executable)
	return CapabilityView{
		Supported:  probe.Supported,
		Reason:     probe.Reason,
		CanRestart: m.cfg.RequestRestart != nil,
	}
}

func (m *Manager) isBusy(err error) bool {
	if m.cfg.IsBusyErr != nil {
		return m.cfg.IsBusyErr(err)
	}
	return false
}
