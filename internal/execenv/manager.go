package execenv

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// EnvironmentSpec holds the stored configuration and secret for one execution environment.
type EnvironmentSpec struct {
	ID        string
	Name      string
	Kind      Kind
	Config    string
	Secret    string
	UpdatedAt int64
}

// TestResult reports connectivity and platform details for an environment probe.
type TestResult struct {
	OK       bool     `json:"ok"`
	Platform string   `json:"platform,omitempty"`
	Arch     string   `json:"arch,omitempty"`
	Home     string   `json:"home,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// ManagerConfig configures the execution environment Manager.
type ManagerConfig struct {
	DataDir       string
	Home          string
	Queries       *sqlc.Queries
	SSHProgram    string
	E2BAPIURL     string
	E2BEnvdURL    string
	E2BHTTPClient *http.Client
}

// Manager resolves each agent's configured execution environment from control storage.
type Manager struct {
	dataDir string
	q       *sqlc.Queries
	local   Provider
	pienv   *piEnvRuntime
	e2b     *e2bRuntime

	mu      sync.Mutex
	closed  bool
	handles map[Handle]struct{}
}

var (
	_ Provider      = (*Manager)(nil)
	_ StampProvider = (*Manager)(nil)
)

// NewManager constructs an execution environment Manager supporting local, pi-env, and e2b backends.
func NewManager(cfg ManagerConfig) *Manager {
	dataDir := filepath.Clean(cfg.DataDir)
	return &Manager{
		dataDir: dataDir,
		q:       cfg.Queries,
		local:   NewLocalProvider(dataDir, cfg.Home),
		pienv:   &piEnvRuntime{dataDir: dataDir, sshProgram: cfg.SSHProgram},
		e2b:     newE2BRuntime(cfg.Queries, cfg.E2BAPIURL, cfg.E2BEnvdURL, cfg.E2BHTTPClient),
		handles: map[Handle]struct{}{},
	}
}

// ValidateEnvironment checks spec against its backend configuration rules.
func (m *Manager) ValidateEnvironment(_ context.Context, spec EnvironmentSpec) error {
	switch spec.Kind {
	case KindPiEnv:
		cfg, err := ParsePiEnvConfig(spec.Config)
		if err != nil {
			return err
		}
		return ValidatePiEnvConfig(m.dataDir, spec.ID, cfg, m.pienv.sshProgram)
	case KindE2B:
		_, err := m.e2b.validate(spec)
		return err
	case KindLocal:
		return fmt.Errorf("%w: unsupported environment kind %q", ErrInvalidConfig, spec.Kind)
	default:
		return fmt.Errorf("%w: unsupported environment kind %q", ErrInvalidConfig, spec.Kind)
	}
}

// TestEnvironment probes connectivity for spec.
func (m *Manager) TestEnvironment(ctx context.Context, spec EnvironmentSpec) (TestResult, error) {
	switch spec.Kind {
	case KindPiEnv:
		cfg, err := ParsePiEnvConfig(spec.Config)
		if err != nil {
			return TestResult{}, err
		}
		return m.pienv.test(ctx, spec.ID, cfg)
	case KindE2B:
		return m.e2b.test(ctx, spec)
	case KindLocal:
		return TestResult{}, fmt.Errorf("%w: unsupported environment kind %q", ErrInvalidConfig, spec.Kind)
	default:
		return TestResult{}, fmt.Errorf("%w: unsupported environment kind %q", ErrInvalidConfig, spec.Kind)
	}
}

// AgentEnvironmentKind returns the configured execution environment Kind for agentID.
func (m *Manager) AgentEnvironmentKind(ctx context.Context, agentID string) (Kind, error) {
	if m.q == nil {
		return KindLocal, nil
	}
	row, err := m.q.GetAgent(ctx, agentID)
	if err != nil {
		return "", err
	}
	if row.EnvironmentID == "" {
		return KindLocal, nil
	}
	envRow, err := m.q.GetEnvironment(ctx, row.EnvironmentID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("execution environment %q not found", row.EnvironmentID)
	}
	if err != nil {
		return "", err
	}
	return Kind(envRow.Kind), nil
}

// ResetAgent kills any persisted E2B sandbox for agentID and clears its sandbox_id.
func (m *Manager) ResetAgent(ctx context.Context, agentID string) error {
	return m.e2b.resetAgent(ctx, agentID)
}

// CleanupSandbox best-effort terminates a previous E2B sandbox after an environment switch or bot archive.
func (m *Manager) CleanupSandbox(ctx context.Context, agentID, envID, sandboxID string) {
	m.e2b.cleanupSandbox(ctx, agentID, envID, sandboxID)
}

// Open resolves agentID's environment_id from SQLite and opens a Handle.
func (m *Manager) Open(ctx context.Context, agentID string) (Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateAgentID(agentID); err != nil {
		return nil, err
	}
	if m.q == nil {
		return m.local.Open(ctx, agentID)
	}
	row, err := m.q.GetAgent(ctx, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("agent %q not found", agentID)
	}
	if err != nil {
		return nil, err
	}
	if row.EnvironmentID == "" {
		return m.local.Open(ctx, agentID)
	}
	envRow, err := m.q.GetEnvironment(ctx, row.EnvironmentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("execution environment %q not found", row.EnvironmentID)
	}
	if err != nil {
		return nil, err
	}

	spec := SpecFromRow(envRow)
	var h Handle
	switch spec.Kind {
	case KindPiEnv:
		cfg, parseErr := ParsePiEnvConfig(spec.Config)
		if parseErr != nil {
			return nil, parseErr
		}
		h, err = m.pienv.open(ctx, agentID, spec.ID, cfg)
	case KindE2B:
		h, err = m.e2b.open(ctx, agentID, spec)
	case KindLocal:
		return nil, fmt.Errorf("%w: unsupported environment kind %q", ErrInvalidConfig, envRow.Kind)
	default:
		return nil, fmt.Errorf("%w: unsupported environment kind %q", ErrInvalidConfig, envRow.Kind)
	}
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = h.Close(context.WithoutCancel(ctx))
		return nil, errors.New("execution environment manager is closed")
	}
	m.handles[h] = struct{}{}
	m.mu.Unlock()
	return &managedHandle{Handle: h, release: func() {
		m.mu.Lock()
		delete(m.handles, h)
		m.mu.Unlock()
	}}, nil
}

// Stamp returns a deterministic fingerprint of agentID's configured execution environment.
func (m *Manager) Stamp(ctx context.Context, agentID string) (string, bool) {
	if m.q == nil {
		return "", true
	}
	row, err := m.q.GetAgentEnvironmentStamp(ctx, agentID)
	if err != nil {
		return "", false
	}
	if row.EnvironmentID == "" {
		return "local", true
	}
	h := sha256.New()
	for _, s := range []string{row.EnvironmentID, row.Kind, row.Config, row.Secret, strconv.FormatInt(row.UpdatedAt, 10)} {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

// Close closes all active handles.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	handles := make([]Handle, 0, len(m.handles))
	for h := range m.handles {
		handles = append(handles, h)
	}
	clear(m.handles)
	m.mu.Unlock()

	var errs []error
	for _, h := range handles {
		errs = append(errs, h.Close(ctx))
	}
	errs = append(errs, m.local.Close(ctx))
	return errors.Join(errs...)
}

// SpecFromRow converts a sqlc.ExecutionEnvironment row into an EnvironmentSpec.
func SpecFromRow(row sqlc.ExecutionEnvironment) EnvironmentSpec {
	return EnvironmentSpec{
		ID:        row.ID,
		Name:      row.Name,
		Kind:      Kind(row.Kind),
		Config:    row.Config,
		Secret:    row.Secret,
		UpdatedAt: row.UpdatedAt,
	}
}

type managedHandle struct {
	Handle
	once    sync.Once
	release func()
}

func (h *managedHandle) Close(ctx context.Context) error {
	h.once.Do(h.release)
	return h.Handle.Close(ctx)
}
