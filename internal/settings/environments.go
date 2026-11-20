package settings

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/execenv"
)

var (
	// ErrNotFound means the requested execution environment does not exist.
	ErrNotFound = errors.New("execution environment not found")
	// ErrConflict means an execution environment cannot be modified or deleted in its current state.
	ErrConflict = errors.New("execution environment conflict")
)

// Environment is the public view of a saved execution environment without its write-only secret.
type Environment struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Kind      execenv.Kind   `json:"kind"`
	Config    jsontext.Value `json:"config"`
	SecretSet bool           `json:"secretSet"`
	CreatedAt int64          `json:"createdAt"`
	UpdatedAt int64          `json:"updatedAt"`
}

// EnvironmentInput creates or updates an execution environment.
type EnvironmentInput struct {
	Name   string
	Kind   string
	Config []byte
	Secret string
}

// ListEnvironments returns all configured execution environments in creation order.
func (s *Service) ListEnvironments(ctx context.Context) ([]Environment, error) {
	rows, err := s.q.ListEnvironments(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Environment, 0, len(rows))
	for _, row := range rows {
		env, err := environmentView(row)
		if err != nil {
			return nil, err
		}
		out = append(out, env)
	}
	return out, nil
}

// CreateEnvironment validates and persists a new execution environment.
func (s *Service) CreateEnvironment(ctx context.Context, in EnvironmentInput) (Environment, error) {
	id := uuid.NewV7().String()
	name, kind, configJSON, err := s.prepareEnvironmentInput(id, in, in.Secret)
	if err != nil {
		return Environment{}, err
	}
	now := time.Now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.q.CreateEnvironment(ctx, sqlc.CreateEnvironmentParams{
		ID:        id,
		Name:      name,
		Kind:      string(kind),
		Config:    configJSON,
		Secret:    strings.TrimSpace(in.Secret),
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return Environment{}, err
	}
	return environmentView(row)
}

// UpdateEnvironment validates and updates an existing execution environment.
func (s *Service) UpdateEnvironment(ctx context.Context, id string, in EnvironmentInput) (Environment, error) {
	if strings.TrimSpace(id) == "" {
		return Environment{}, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	saved, err := s.q.GetEnvironment(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Environment{}, ErrNotFound
	}
	if err != nil {
		return Environment{}, err
	}
	if strings.TrimSpace(in.Kind) != saved.Kind {
		return Environment{}, fmt.Errorf("%w: environment kind cannot change after creation", ErrInvalid)
	}
	secret := saved.Secret
	if trimmed := strings.TrimSpace(in.Secret); trimmed != "" {
		secret = trimmed
	}
	name, kind, configJSON, err := s.prepareEnvironmentInput(id, in, secret)
	if err != nil {
		return Environment{}, err
	}
	row, err := s.q.UpdateEnvironment(ctx, sqlc.UpdateEnvironmentParams{
		Name:      name,
		Kind:      string(kind),
		Config:    configJSON,
		Secret:    secret,
		UpdatedAt: time.Now().UnixMilli(),
		ID:        id,
	})
	if err != nil {
		return Environment{}, err
	}
	_ = s.envs.Changed(ctx, execenv.SpecFromRow(saved), new(execenv.SpecFromRow(row)))
	return environmentView(row)
}

// DeleteEnvironment removes an execution environment unless an active bot references it.
func (s *Service) DeleteEnvironment(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	saved, err := s.q.GetEnvironment(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	rows, err := s.q.DeleteEnvironmentIfUnused(ctx, id)
	if err != nil {
		return err
	}
	if rows == 0 {
		if _, getErr := s.q.GetEnvironment(ctx, id); errors.Is(getErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		users, listErr := s.q.ListActiveAgentsByEnvironment(ctx, id)
		if listErr != nil {
			return listErr
		}
		return fmt.Errorf("%w: execution environment %q is used by %d active bot(s)", ErrConflict, saved.Name, max(1, len(users)))
	}
	if s.envs != nil {
		_ = s.envs.Changed(ctx, execenv.SpecFromRow(saved), nil)
	}
	return nil
}

// TestEnvironment probes connectivity and platform metadata for environment id.
func (s *Service) TestEnvironment(ctx context.Context, id string) (execenv.TestResult, error) {
	if strings.TrimSpace(id) == "" {
		return execenv.TestResult{}, ErrNotFound
	}
	row, err := s.q.GetEnvironment(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return execenv.TestResult{}, ErrNotFound
	}
	if err != nil {
		return execenv.TestResult{}, err
	}
	if s.envs == nil {
		return execenv.TestResult{}, errors.New("execution environment manager is not configured")
	}
	spec := execenv.SpecFromRow(row)
	res, err := s.envs.Probe(ctx, &spec, spec)
	return res, wrapInvalidConfig(err)
}

// DiscoverSSHConfig discovers concrete host aliases and candidate identity files from the control host's ~/.ssh.
func (s *Service) DiscoverSSHConfig(_ context.Context) (execenv.SSHConfigDiscovery, error) {
	if s.envs == nil {
		return execenv.SSHConfigDiscovery{Hosts: []execenv.SSHHostEntry{}, IdentityFiles: []string{}}, nil
	}
	return s.envs.DiscoverSSHConfig()
}

// ProbeEnvironment tests connectivity for an unsaved or edited environment input without persisting changes.
func (s *Service) ProbeEnvironment(ctx context.Context, existingID string, in EnvironmentInput) (execenv.TestResult, error) {
	if s.envs == nil {
		return execenv.TestResult{}, errors.New("execution environment manager is not configured")
	}
	id := strings.TrimSpace(existingID)
	kind := execenv.Kind(strings.TrimSpace(in.Kind))
	secret := strings.TrimSpace(in.Secret)
	var savedSpec *execenv.EnvironmentSpec
	if id != "" {
		saved, err := s.q.GetEnvironment(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return execenv.TestResult{}, ErrNotFound
		}
		if err != nil {
			return execenv.TestResult{}, err
		}
		spec := execenv.SpecFromRow(saved)
		savedSpec = &spec
		if secret == "" && spec.Kind == kind {
			secret = spec.Secret
		}
	}
	res, err := s.envs.Probe(ctx, savedSpec, execenv.EnvironmentSpec{
		ID:     id,
		Kind:   kind,
		Config: normalizeRawJSON(in.Config),
		Secret: secret,
	})
	return res, wrapInvalidConfig(err)
}

// ForgetEnvironmentHostKey removes saved SSH known_hosts entries for environment id so the next connect re-trusts via TOFU.
func (s *Service) ForgetEnvironmentHostKey(ctx context.Context, id string) error {
	trimmedID := strings.TrimSpace(id)
	if trimmedID == "" {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	saved, err := s.q.GetEnvironment(ctx, trimmedID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if s.envs == nil {
		return errors.New("execution environment manager is not configured")
	}
	return wrapInvalidConfig(s.envs.ForgetHostKey(execenv.SpecFromRow(saved)))
}

func (s *Service) prepareEnvironmentInput(envID string, in EnvironmentInput, secret string) (string, execenv.Kind, string, error) {
	name := strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 60 {
		return "", "", "", fmt.Errorf("%w: name must be 1 to 60 characters", ErrInvalid)
	}
	if s.envs == nil {
		return "", "", "", errors.New("execution environment manager is not configured")
	}
	kind := execenv.Kind(strings.TrimSpace(in.Kind))
	normalizedCfg, err := s.envs.Normalize(execenv.EnvironmentSpec{
		ID:     envID,
		Kind:   kind,
		Config: normalizeRawJSON(in.Config),
		Secret: strings.TrimSpace(secret),
	})
	if err != nil {
		return "", "", "", wrapInvalidConfig(err)
	}
	return name, kind, normalizedCfg, nil
}

func normalizeRawJSON(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "{}"
	}
	return trimmed
}

func wrapInvalidConfig(err error) error {
	if errors.Is(err, execenv.ErrInvalidConfig) {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return err
}

func environmentView(row sqlc.ExecutionEnvironment) (Environment, error) {
	rawCfg := strings.TrimSpace(row.Config)
	if rawCfg == "" || rawCfg == "null" {
		rawCfg = "{}"
	}
	cfg := jsontext.Value(rawCfg)
	if !cfg.IsValid() {
		return Environment{}, fmt.Errorf("%w: invalid stored environment config", ErrInvalid)
	}
	return Environment{
		ID:        row.ID,
		Name:      row.Name,
		Kind:      execenv.Kind(row.Kind),
		Config:    cfg,
		SecretSet: row.Secret != "",
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}, nil
}

// AllowedEnvironments lists the execution environments that sessions of agentID can select: its
// environment first, then its allowlist. It has no configuration and no secrets.
func (s *Service) AllowedEnvironments(ctx context.Context, agentID string) ([]engine.EnvironmentInfo, error) {
	rows, err := s.q.ListAllowedEnvironments(ctx, agentID)
	if err != nil {
		return nil, err
	}
	out := make([]engine.EnvironmentInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, engine.EnvironmentInfo{ID: r.ID, Name: r.Name, Kind: r.Kind, Default: r.IsDefault == 1, Status: s.envs.Status(ctx, agentID, r.ID)})
	}
	return out, nil
}

// SetAllowedEnvironments replaces the allowlist of agentID. A removed environment stops the tools
// of the sessions that select it, and the backend releases the state of the agent in it.
func (s *Service) SetAllowedEnvironments(ctx context.Context, agentID string, ids []string) ([]engine.EnvironmentInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != execenv.LocalID {
			if _, err := s.q.GetEnvironment(ctx, id); errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("%w: execution environment %q does not exist", ErrInvalid, id)
			} else if err != nil {
				return nil, err
			}
		}
		want[id] = true
	}
	agent, err := s.q.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	current, err := s.q.ListAgentEnvironments(ctx, agentID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	for id := range want {
		if err := s.q.AddAgentEnvironment(ctx, sqlc.AddAgentEnvironmentParams{AgentID: agentID, EnvironmentID: id, CreatedAt: now}); err != nil {
			if strings.Contains(err.Error(), "runner environment already has an active bot") {
				return nil, fmt.Errorf("%w: execution environment %q already serves another bot", ErrConflict, id)
			}
			return nil, err
		}
	}
	for _, id := range current {
		if want[id] {
			continue
		}
		if err := s.q.RemoveAgentEnvironment(ctx, sqlc.RemoveAgentEnvironmentParams{AgentID: agentID, EnvironmentID: id}); err != nil {
			return nil, err
		}
		if id == agent.EnvironmentID {
			continue // the environment of the agent stays in use
		}
		if err := s.envs.ReleaseAgent(ctx, agentID, id); err != nil {
			return nil, fmt.Errorf("release execution environment %q: %w", id, err)
		}
	}
	return s.AllowedEnvironments(ctx, agentID)
}
