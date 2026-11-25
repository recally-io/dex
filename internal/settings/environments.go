package settings

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/runner"
)

var (
	// ErrNotFound means the requested execution environment does not exist.
	ErrNotFound = errors.New("execution environment not found")
	// ErrConflict means an execution environment cannot be modified or deleted in its current state.
	ErrConflict = errors.New("execution environment conflict")
)

// Environment is the public view of a saved execution environment without its write-only secret.
type Environment struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"desc,omitempty"`
	Kind        runner.Kind    `json:"kind"`
	Config      jsontext.Value `json:"config"`
	SecretSet   bool           `json:"secretSet"`
	CreatedAt   int64          `json:"createdAt"`
	UpdatedAt   int64          `json:"updatedAt"`
}

// EnvironmentInput creates or updates an execution environment.
type EnvironmentInput struct {
	Name        string
	Description *string
	Kind        string
	Config      []byte
	Secret      string
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
	desc, err := normalizeEnvironmentDescription(in.Description)
	if err != nil {
		return Environment{}, err
	}
	id := uuid.NewV7().String()
	name, kind, configJSON, err := s.prepareEnvironmentInput(id, in, in.Secret)
	if err != nil {
		return Environment{}, err
	}
	now := time.Now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.q.CreateEnvironment(ctx, sqlc.CreateEnvironmentParams{
		ID:          id,
		Name:        name,
		Description: desc.String,
		Kind:        string(kind),
		Config:      configJSON,
		Secret:      strings.TrimSpace(in.Secret),
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		return Environment{}, err
	}
	return environmentView(row)
}

// UpdateEnvironment saves an environment and reports whether to invalidate runtime state.
func (s *Service) UpdateEnvironment(ctx context.Context, id string, in EnvironmentInput) (Environment, bool, error) {
	if strings.TrimSpace(id) == "" {
		return Environment{}, false, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	saved, err := s.q.GetEnvironment(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Environment{}, false, ErrNotFound
	}
	if err != nil {
		return Environment{}, false, err
	}
	if strings.TrimSpace(in.Kind) != saved.Kind {
		return Environment{}, false, fmt.Errorf("%w: environment kind cannot change after creation", ErrInvalid)
	}
	desc, err := normalizeEnvironmentDescription(in.Description)
	if err != nil {
		return Environment{}, false, err
	}
	secret := saved.Secret
	if trimmed := strings.TrimSpace(in.Secret); trimmed != "" {
		secret = trimmed
	}
	name, kind, configJSON, err := s.prepareEnvironmentInput(id, in, secret)
	if err != nil {
		return Environment{}, false, err
	}
	// The runtime uses UpdatedAt in environment stamps. Keep it for description-only saves.
	runtimeChanged := !desc.Valid || name != saved.Name || configJSON != saved.Config || secret != saved.Secret
	updatedAt := saved.UpdatedAt
	if runtimeChanged {
		updatedAt = time.Now().UnixMilli()
	}
	row, err := s.q.UpdateEnvironment(ctx, sqlc.UpdateEnvironmentParams{
		Name:        name,
		Description: desc,
		Kind:        string(kind),
		Config:      configJSON,
		Secret:      secret,
		UpdatedAt:   updatedAt,
		ID:          id,
	})
	if err != nil {
		return Environment{}, false, err
	}
	if runtimeChanged {
		_ = s.envs.Changed(ctx, runner.SpecFromRow(saved), new(runner.SpecFromRow(row)))
	}
	view, err := environmentView(row)
	return view, runtimeChanged, err
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
		_ = s.envs.Changed(ctx, runner.SpecFromRow(saved), nil)
	}
	return nil
}

// TestEnvironment probes connectivity and platform metadata for environment id.
func (s *Service) TestEnvironment(ctx context.Context, id string) (runner.TestResult, error) {
	if strings.TrimSpace(id) == "" {
		return runner.TestResult{}, ErrNotFound
	}
	row, err := s.q.GetEnvironment(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return runner.TestResult{}, ErrNotFound
	}
	if err != nil {
		return runner.TestResult{}, err
	}
	if s.envs == nil {
		return runner.TestResult{}, errors.New("execution environment manager is not configured")
	}
	spec := runner.SpecFromRow(row)
	res, err := s.envs.Probe(ctx, &spec, spec)
	return res, wrapInvalidConfig(err)
}

// DiscoverSSHConfig discovers concrete host aliases and candidate identity files from the control host's ~/.ssh.
func (s *Service) DiscoverSSHConfig(_ context.Context) (runner.SSHConfigDiscovery, error) {
	if s.envs == nil {
		return runner.SSHConfigDiscovery{Hosts: []runner.SSHHostEntry{}, IdentityFiles: []string{}}, nil
	}
	return s.envs.DiscoverSSHConfig()
}

// ProbeEnvironment tests connectivity for an unsaved or edited environment input without persisting changes.
func (s *Service) ProbeEnvironment(ctx context.Context, existingID string, in EnvironmentInput) (runner.TestResult, error) {
	if s.envs == nil {
		return runner.TestResult{}, errors.New("execution environment manager is not configured")
	}
	id := strings.TrimSpace(existingID)
	kind := runner.Kind(strings.TrimSpace(in.Kind))
	secret := strings.TrimSpace(in.Secret)
	var savedSpec *runner.EnvironmentSpec
	if id != "" {
		saved, err := s.q.GetEnvironment(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return runner.TestResult{}, ErrNotFound
		}
		if err != nil {
			return runner.TestResult{}, err
		}
		spec := runner.SpecFromRow(saved)
		savedSpec = &spec
		if secret == "" && spec.Kind == kind {
			secret = spec.Secret
		}
	}
	res, err := s.envs.Probe(ctx, savedSpec, runner.EnvironmentSpec{
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
	return wrapInvalidConfig(s.envs.ForgetHostKey(runner.SpecFromRow(saved)))
}

func (s *Service) prepareEnvironmentInput(envID string, in EnvironmentInput, secret string) (string, runner.Kind, string, error) {
	name := strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 60 {
		return "", "", "", fmt.Errorf("%w: name must be 1 to 60 characters", ErrInvalid)
	}
	if s.envs == nil {
		return "", "", "", errors.New("execution environment manager is not configured")
	}
	kind := runner.Kind(strings.TrimSpace(in.Kind))
	normalizedCfg, err := s.envs.Normalize(runner.EnvironmentSpec{
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

func normalizeEnvironmentDescription(raw *string) (sql.NullString, error) {
	if raw == nil {
		return sql.NullString{}, nil
	}
	if utf8.RuneCountInString(*raw) > 500 {
		return sql.NullString{}, fmt.Errorf("%w: description must be at most 500 characters", ErrInvalid)
	}
	desc := strings.TrimSpace(strings.ReplaceAll(*raw, "\r\n", "\n"))
	if strings.ContainsFunc(desc, func(r rune) bool { return r != '\n' && r != '\t' && unicode.IsControl(r) }) {
		return sql.NullString{}, fmt.Errorf("%w: description must be plain text", ErrInvalid)
	}
	return sql.NullString{String: desc, Valid: true}, nil
}

func normalizeRawJSON(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "{}"
	}
	return trimmed
}

func wrapInvalidConfig(err error) error {
	if errors.Is(err, runner.ErrInvalidConfig) {
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
		ID:          row.ID,
		Name:        row.Name,
		Description: row.Description,
		Kind:        runner.Kind(row.Kind),
		Config:      cfg,
		SecretSet:   row.Secret != "",
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}, nil
}

// SessionEnvironments lists the execution environments for the sessions of agentID: the
// environment of the bot first, then local and the other registered environments.
// It has no configuration and no secrets.
func (s *Service) SessionEnvironments(ctx context.Context, agentID string) ([]engine.EnvironmentInfo, error) {
	agent, err := s.q.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListEnvironments(ctx)
	if err != nil {
		return nil, err
	}
	own := engine.EnvironmentInfo{ID: runner.LocalID, Name: runner.LocalID, Kind: string(runner.KindLocal), Default: true, Status: s.envs.Status(ctx, agentID, "")}
	out := []engine.EnvironmentInfo{own}
	if agent.EnvironmentID != "" {
		out = append(out, engine.EnvironmentInfo{ID: runner.LocalID, Name: runner.LocalID, Kind: string(runner.KindLocal), Status: "available"})
	}
	for _, r := range rows {
		if r.ID == agent.EnvironmentID {
			out[0].ID, out[0].Name, out[0].Kind, out[0].Description = r.ID, r.Name, r.Kind, r.Description
			continue
		}
		if runner.IsRegisteredKind(runner.Kind(r.Kind)) {
			out = append(out, engine.EnvironmentInfo{ID: r.ID, Name: r.Name, Description: r.Description, Kind: r.Kind, Status: s.envs.Status(ctx, agentID, r.ID)})
		}
	}
	return out, nil
}
