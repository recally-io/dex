package execenv

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// EnvironmentSpec holds the stored configuration and secret for one execution environment.
type EnvironmentSpec struct {
	ID        string
	Kind      Kind
	Config    string
	Secret    string
	UpdatedAt int64
}

// TestResult reports connectivity and platform details for an environment probe.
type TestResult struct {
	OK             bool     `json:"ok"`
	Platform       string   `json:"platform,omitempty"`
	Arch           string   `json:"arch,omitempty"`
	Home           string   `json:"home,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
	HostKeyChanged bool     `json:"hostKeyChanged,omitzero"`
	Error          string   `json:"error,omitempty"`
}

// ManagerConfig configures the execution environment Manager.
type ManagerConfig struct {
	DataDir string
	// SSHHome overrides the directory scanned for ~/.ssh/config and key discovery (falls back to OS home).
	SSHHome    string
	Queries    *sqlc.Queries
	SSHProgram string
	E2BAPIURL  string
	E2BEnvdURL string
}

// Manager resolves each agent's configured execution environment from control storage.
type Manager struct {
	dataDir string
	sshHome string
	q       *sqlc.Queries
	local   Provider
	pienv   *piEnvRuntime
	e2b     *e2bRuntime

	mu      sync.Mutex
	closed  bool
	handles map[Handle]struct{}
}

var _ Provider = (*Manager)(nil)

// NewManager constructs an execution environment Manager supporting local, SSH (pi-env), and E2B sandbox backends.
func NewManager(cfg ManagerConfig) *Manager {
	dataDir := filepath.Clean(cfg.DataDir)
	return &Manager{
		dataDir: dataDir,
		sshHome: cfg.SSHHome,
		q:       cfg.Queries,
		local:   NewLocalProvider(dataDir, ""),
		pienv:   &piEnvRuntime{dataDir: dataDir, sshProgram: cfg.SSHProgram},
		e2b:     newE2BRuntime(cfg.Queries, cfg.E2BAPIURL, cfg.E2BEnvdURL),
		handles: map[Handle]struct{}{},
	}
}

func unsupportedKindErr(kind Kind) error {
	return fmt.Errorf("%w: unsupported environment kind %q", ErrInvalidConfig, kind)
}

// DiscoverSSHConfig discovers concrete host entries and private key path candidates under the configured SSH home directory.
func (m *Manager) DiscoverSSHConfig() (SSHConfigDiscovery, error) {
	return DiscoverSSHConfig(m.sshHome)
}

func (m *Manager) canReset(kind Kind) bool {
	return kind == KindE2B
}

// CanResetAgent reports whether agentID's configured execution environment supports stateless reset.
func (m *Manager) CanResetAgent(ctx context.Context, agentID string) (bool, error) {
	kind, err := m.agentEnvironmentKind(ctx, agentID)
	if err != nil {
		return false, err
	}
	return m.canReset(kind), nil
}

// NormalizeAndValidate validates spec against its backend rules and returns the normalized JSON config.
func (m *Manager) NormalizeAndValidate(ctx context.Context, spec EnvironmentSpec) (string, error) {
	switch spec.Kind {
	case KindPiEnv:
		return m.pienv.normalizeAndValidate(ctx, spec)
	case KindE2B:
		return m.e2b.normalizeAndValidate(ctx, spec)
	case KindLocal:
		return "", unsupportedKindErr(spec.Kind)
	default:
		return "", unsupportedKindErr(spec.Kind)
	}
}

// TestEnvironment probes connectivity for a saved environment spec.
func (m *Manager) TestEnvironment(ctx context.Context, spec EnvironmentSpec) (TestResult, error) {
	return m.ProbeEnvironment(ctx, &spec, spec)
}

// ProbeEnvironment probes connectivity for spec. When saved is provided, drivers may reuse saved host state (such as SSH known_hosts) if the endpoint is unchanged.
func (m *Manager) ProbeEnvironment(ctx context.Context, saved *EnvironmentSpec, spec EnvironmentSpec) (TestResult, error) {
	switch spec.Kind {
	case KindPiEnv:
		return m.pienv.probe(ctx, saved, spec)
	case KindE2B:
		return m.e2b.probe(ctx, saved, spec)
	case KindLocal:
		return TestResult{}, unsupportedKindErr(spec.Kind)
	default:
		return TestResult{}, unsupportedKindErr(spec.Kind)
	}
}

// OnEnvironmentUpdated runs backend-specific housekeeping after an environment configuration changes.
func (m *Manager) OnEnvironmentUpdated(oldSpec, newSpec EnvironmentSpec) error {
	switch newSpec.Kind {
	case KindPiEnv:
		return m.pienv.onUpdated(oldSpec, newSpec)
	case KindE2B:
		return nil
	case KindLocal:
		return unsupportedKindErr(newSpec.Kind)
	default:
		return unsupportedKindErr(newSpec.Kind)
	}
}

// OnEnvironmentDeleted runs backend-specific cleanup after an environment row is removed.
func (m *Manager) OnEnvironmentDeleted(spec EnvironmentSpec) error {
	switch spec.Kind {
	case KindPiEnv:
		return m.pienv.onDeleted(spec)
	case KindE2B:
		return nil
	case KindLocal:
		return unsupportedKindErr(spec.Kind)
	default:
		return unsupportedKindErr(spec.Kind)
	}
}

// ForgetHostKey removes saved SSH host trust entries for spec so the next connect re-trusts via TOFU.
func (m *Manager) ForgetHostKey(spec EnvironmentSpec) error {
	if spec.Kind != KindPiEnv {
		return fmt.Errorf("%w: host key reset applies only to SSH environments", ErrInvalidConfig)
	}
	return m.pienv.forgetHostKey(spec)
}

func (m *Manager) agentEnvironmentKind(ctx context.Context, agentID string) (Kind, error) {
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

// ResetAgent kills any persisted disposable sandbox for agentID and clears its sandbox reference.
func (m *Manager) ResetAgent(ctx context.Context, agentID string) error {
	kind, err := m.agentEnvironmentKind(ctx, agentID)
	if err != nil {
		return err
	}
	if !m.canReset(kind) {
		return nil
	}
	return m.e2b.resetAgent(ctx, agentID)
}

// CleanupSandbox best-effort terminates a previous sandbox after an environment switch or bot archive.
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
		h, err = m.pienv.open(ctx, agentID, spec)
	case KindE2B:
		h, err = m.e2b.open(ctx, agentID, spec)
	case KindLocal:
		err = unsupportedKindErr(spec.Kind)
	default:
		err = unsupportedKindErr(spec.Kind)
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
