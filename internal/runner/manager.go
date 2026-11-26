package runner

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

// Spec holds the stored configuration and secret for one execution environment.
type Spec struct {
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
	e2b      *e2bRuntime
	backends map[Kind]backend
}

var _ Provider = (*Manager)(nil)

// NewManager supports Local, SSH, E2B, and Outbound Runners.
func NewManager(cfg ManagerConfig) *Manager {
	dataDir := filepath.Clean(cfg.DataDir)
	pienv := newPiEnvRuntime(dataDir, cfg.SSHProgram)
	runners := newRunners(cfg.Queries)
	e2b := newE2BRuntime(cfg.Queries, cfg.E2BAPIURL, cfg.E2BEnvdURL)
	return &Manager{
		sshHome: cfg.SSHHome,
		q:       cfg.Queries,
		local:   newLocalProvider(dataDir, ""),
		pienv:   pienv,
		runners: runners,
		e2b:     e2b,
		backends: map[Kind]backend{
			KindSSH:    pienv,
			KindE2B:    e2b,
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
func (m *Manager) Normalize(spec Spec) (string, error) {
	b, err := m.backend(spec.Kind)
	if err != nil {
		return "", err
	}
	return b.normalize(spec)
}

// Probe checks connectivity for spec. When saved is the stored spec of the same environment,
// a backend can reuse saved host state, such as SSH known_hosts, if the endpoint is unchanged.
func (m *Manager) Probe(ctx context.Context, saved *Spec, spec Spec) (TestResult, error) {
	b, err := m.backend(spec.Kind)
	if err != nil {
		return TestResult{}, err
	}
	return b.probe(ctx, saved, spec)
}

// Changed runs backend housekeeping after an environment row changed. cur is nil when the row was deleted.
func (m *Manager) Changed(ctx context.Context, old Spec, cur *Spec) error {
	b, err := m.backend(old.Kind)
	if err != nil {
		return err
	}
	return b.changed(ctx, old, cur)
}

// ForgetHostKey removes saved SSH host trust entries for spec so the next connect re-trusts via TOFU.
func (m *Manager) ForgetHostKey(spec Spec) error {
	if spec.Kind != KindSSH {
		return fmt.Errorf("%w: host key reset applies only to SSH environments", ErrInvalidConfig)
	}
	return m.pienv.forgetHostKey(spec)
}

// binding is the environment that an agent uses. A zero spec selects local.
type binding struct {
	spec      Spec
	sandboxID string
}

// binding resolves runnerID for agentID. Empty selects the environment of the agent, with
// its sandbox. An explicit selection names local or a registered environment.
func (m *Manager) binding(ctx context.Context, agentID, runnerID string) (binding, error) {
	switch runnerID {
	case "":
		return m.agentBinding(ctx, agentID)
	case LocalID:
		return binding{}, nil
	}
	row, err := m.q.GetRunner(ctx, runnerID)
	if errors.Is(err, sql.ErrNoRows) {
		return binding{}, fmt.Errorf("%w: execution environment %s was removed", ErrUnavailable, runnerID)
	}
	if err != nil {
		return binding{}, err
	}
	if !IsRegisteredKind(Kind(row.Kind)) {
		return binding{}, fmt.Errorf("%w: %s has unsupported environment kind %s", ErrUnavailable, row.Name, row.Kind)
	}
	return binding{spec: SpecFromRow(row)}, nil
}

func (m *Manager) agentBinding(ctx context.Context, agentID string) (binding, error) {
	row, err := m.q.GetAgentBinding(ctx, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return binding{}, fmt.Errorf("agent %q not found", agentID)
	}
	if err != nil {
		return binding{}, err
	}
	if row.RunnerID == "" {
		return binding{}, nil
	}
	if row.FoundID == "" {
		return binding{}, fmt.Errorf("%w: execution environment %s was removed", ErrUnavailable, row.RunnerID)
	}
	spec := Spec{ID: row.FoundID, Kind: Kind(row.Kind), Config: row.Config, Secret: row.Secret, UpdatedAt: row.UpdatedAt}
	return binding{spec: spec, sandboxID: row.RunnerSandboxID}, nil
}

// stamp fingerprints spec and the backend identity, so a new machine enrollment or a forgotten
// host key makes earlier handles stale. A machine reconnect keeps the stamp.
func (m *Manager) stamp(ctx context.Context, b backend, spec Spec) (string, error) {
	identity, err := b.identity(ctx, spec.ID)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, s := range []string{spec.ID, string(spec.Kind), spec.Config, spec.Secret, strconv.FormatInt(spec.UpdatedAt, 10), identity} {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Open resolves the selected environment of agentID from control storage and opens a lease on it.
func (m *Manager) Open(ctx context.Context, agentID, runnerID string) (Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateAgentID(agentID); err != nil {
		return nil, err
	}
	bound, err := m.binding(ctx, agentID, runnerID)
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
	// Stamp before the open: an identity change during the open makes this handle stale, never fresh.
	stamp, err := m.stamp(ctx, b, bound.spec)
	if err != nil {
		return nil, err
	}
	var l lease
	if bound.spec.Kind == KindE2B && runnerID != "" {
		agent, getErr := m.q.GetAgent(ctx, agentID)
		if getErr != nil {
			return nil, getErr
		}
		if agent.RunnerID != runnerID {
			l, err = m.e2b.openOverride(ctx, agentID, bound.spec)
		} else {
			l, err = b.open(ctx, agentID, bound.spec)
		}
	} else {
		l, err = b.open(ctx, agentID, bound.spec)
	}
	if err != nil {
		return nil, err
	}
	return stampedHandle{lease: l, stamp: stamp}, nil
}

// Stamp returns the fingerprint of the selected execution environment of agentID.
func (m *Manager) Stamp(ctx context.Context, agentID, runnerID string) (string, error) {
	bound, err := m.binding(ctx, agentID, runnerID)
	if err != nil {
		return "", err
	}
	if bound.spec.ID == "" {
		return localStamp, nil
	}
	b, err := m.backend(bound.spec.Kind)
	if err != nil {
		return "", err
	}
	return m.stamp(ctx, b, bound.spec)
}

// Status reports whether agentID can use runnerID now: "available", "offline" for a
// disconnected machine, or "unavailable: <reason>".
func (m *Manager) Status(ctx context.Context, agentID, runnerID string) string {
	bound, err := m.binding(ctx, agentID, runnerID)
	if err != nil {
		return "unavailable: " + err.Error()
	}
	if bound.spec.Kind == KindRunner && m.runners.online(bound.spec.ID) == nil {
		return "offline"
	}
	return "available"
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
	envRow, err := m.q.GetRunner(ctx, envID)
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

// SpecFromRow converts a sqlc.Runner row into an Spec.
func SpecFromRow(row sqlc.Runner) Spec {
	return Spec{
		ID:        row.ID,
		Kind:      Kind(row.Kind),
		Config:    row.Config,
		Secret:    row.Secret,
		UpdatedAt: row.UpdatedAt,
	}
}
