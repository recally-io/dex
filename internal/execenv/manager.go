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
	SSHHome string
	// Queries reads agents and environments. Probes work without it; Open, Stamp, and the agent methods need it.
	Queries    *sqlc.Queries
	SSHProgram string
	E2BAPIURL  string
	E2BEnvdURL string
}

// Manager resolves each agent's configured execution environment from control storage
// and dispatches each environment to its backend.
type Manager struct {
	sshHome  string
	q        *sqlc.Queries
	local    *localProvider
	pienv    *piEnvRuntime
	runners  *Runners
	backends map[Kind]backend
}

var _ Provider = (*Manager)(nil)

// NewManager constructs an execution environment Manager supporting local, SSH (pi-env), E2B, and machine backends.
func NewManager(cfg ManagerConfig) *Manager {
	dataDir := filepath.Clean(cfg.DataDir)
	pienv := newPiEnvRuntime(dataDir, cfg.SSHProgram)
	runners := newRunners(cfg.Queries)
	return &Manager{
		sshHome: cfg.SSHHome,
		q:       cfg.Queries,
		local:   newLocalProvider(dataDir, ""),
		pienv:   pienv,
		runners: runners,
		backends: map[Kind]backend{
			KindPiEnv:  pienv,
			KindE2B:    newE2BRuntime(cfg.Queries, cfg.E2BAPIURL, cfg.E2BEnvdURL),
			KindRunner: runnerBackend{runners: runners},
		},
	}
}

// Runners returns the enrollment and live sessions of remote machines.
func (m *Manager) Runners() *Runners { return m.runners }

// DiscoverSSHConfig discovers concrete host entries and private key path candidates under the configured SSH home directory.
func (m *Manager) DiscoverSSHConfig() (SSHConfigDiscovery, error) {
	return DiscoverSSHConfig(m.sshHome)
}

func (m *Manager) backend(kind Kind) (backend, error) {
	if b, ok := m.backends[kind]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("%w: unsupported environment kind %q", ErrInvalidConfig, kind)
}

// Normalize validates spec against its backend rules and returns the normalized JSON config.
func (m *Manager) Normalize(spec EnvironmentSpec) (string, error) {
	b, err := m.backend(spec.Kind)
	if err != nil {
		return "", err
	}
	return b.normalize(spec)
}

// Probe checks connectivity for spec. When saved is the stored spec of the same environment,
// a backend can reuse saved host state, such as SSH known_hosts, if the endpoint is unchanged.
func (m *Manager) Probe(ctx context.Context, saved *EnvironmentSpec, spec EnvironmentSpec) (TestResult, error) {
	b, err := m.backend(spec.Kind)
	if err != nil {
		return TestResult{}, err
	}
	return b.probe(ctx, saved, spec)
}

// Changed runs backend housekeeping after an environment row changed. cur is nil when the row was deleted.
func (m *Manager) Changed(ctx context.Context, old EnvironmentSpec, cur *EnvironmentSpec) error {
	b, err := m.backend(old.Kind)
	if err != nil {
		return err
	}
	return b.changed(ctx, old, cur)
}

// ForgetHostKey removes saved SSH host trust entries for spec so the next connect re-trusts via TOFU.
func (m *Manager) ForgetHostKey(spec EnvironmentSpec) error {
	if spec.Kind != KindPiEnv {
		return fmt.Errorf("%w: host key reset applies only to SSH environments", ErrInvalidConfig)
	}
	return m.pienv.forgetHostKey(spec)
}

// binding is the environment that an agent uses. A zero spec selects local.
type binding struct {
	spec      EnvironmentSpec
	sandboxID string
}

func (m *Manager) agentBinding(ctx context.Context, agentID string) (binding, error) {
	row, err := m.q.GetAgent(ctx, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return binding{}, fmt.Errorf("agent %q not found", agentID)
	}
	if err != nil {
		return binding{}, err
	}
	if row.EnvironmentID == "" {
		return binding{}, nil
	}
	envRow, err := m.q.GetEnvironment(ctx, row.EnvironmentID)
	if errors.Is(err, sql.ErrNoRows) {
		return binding{}, fmt.Errorf("execution environment %q not found", row.EnvironmentID)
	}
	if err != nil {
		return binding{}, err
	}
	return binding{spec: SpecFromRow(envRow), sandboxID: row.EnvironmentSandboxID}, nil
}

// stamp fingerprints spec. A machine session also adds its generation, so a reconnect makes
// earlier handles stale.
func (m *Manager) stamp(spec EnvironmentSpec) string {
	if spec.ID == "" {
		return localStamp
	}
	h := sha256.New()
	for _, s := range []string{spec.ID, string(spec.Kind), spec.Config, spec.Secret, strconv.FormatInt(spec.UpdatedAt, 10)} {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	if spec.Kind == KindRunner {
		_, _ = h.Write([]byte(strconv.FormatUint(m.runners.generation(spec.ID), 10)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Open resolves agentID's environment from control storage and opens a lease on it.
func (m *Manager) Open(ctx context.Context, agentID string) (Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateAgentID(agentID); err != nil {
		return nil, err
	}
	bound, err := m.agentBinding(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if bound.spec.ID == "" {
		l, err := m.local.open(ctx, agentID)
		if err != nil {
			return nil, err
		}
		return stampedHandle{lease: l, stamp: localStamp}, nil
	}
	b, err := m.backend(bound.spec.Kind)
	if err != nil {
		return nil, err
	}
	// Stamp before the open: a machine that reconnects during the open makes this handle stale, never fresh.
	stamp := m.stamp(bound.spec)
	l, err := b.open(ctx, agentID, bound.spec)
	if err != nil {
		return nil, err
	}
	return stampedHandle{lease: l, stamp: stamp}, nil
}

// Stamp returns the fingerprint of agentID's configured execution environment.
func (m *Manager) Stamp(ctx context.Context, agentID string) (string, bool) {
	bound, err := m.agentBinding(ctx, agentID)
	if err != nil {
		return "", false
	}
	return m.stamp(bound.spec), true
}

// CanResetAgent reports whether agentID's environment discards its files on reset.
func (m *Manager) CanResetAgent(ctx context.Context, agentID string) (bool, error) {
	bound, err := m.agentBinding(ctx, agentID)
	if err != nil || bound.spec.ID == "" {
		return false, err
	}
	b, err := m.backend(bound.spec.Kind)
	if err != nil {
		return false, err
	}
	return b.disposable(), nil
}

// ResetAgent discards agentID's state in its current environment when that environment is disposable.
func (m *Manager) ResetAgent(ctx context.Context, agentID string) error {
	bound, err := m.agentBinding(ctx, agentID)
	if err != nil || bound.spec.ID == "" {
		return err
	}
	b, err := m.backend(bound.spec.Kind)
	if err != nil {
		return err
	}
	if !b.disposable() {
		return nil
	}
	return b.release(ctx, agentID, bound.spec, bound.sandboxID)
}

// ReleaseAgent tells the backend of an earlier environment that agentID left it.
// sandboxID is the agent's sandbox reference in that environment, or empty.
func (m *Manager) ReleaseAgent(ctx context.Context, agentID, envID, sandboxID string) error {
	if envID == "" {
		return nil
	}
	envRow, err := m.q.GetEnvironment(ctx, envID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	spec := SpecFromRow(envRow)
	b, err := m.backend(spec.Kind)
	if err != nil {
		return err
	}
	return b.release(ctx, agentID, spec, sandboxID)
}

// Close disconnects the machines and closes the shared connections. Open leases fail after Close.
func (m *Manager) Close(context.Context) error {
	m.runners.close()
	m.pienv.close()
	return nil
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
