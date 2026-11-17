package settings

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/execenv"
)

var (
	// ErrNotFound means the requested execution environment does not exist.
	ErrNotFound = errors.New("execution environment not found")
	// ErrConflict means an execution environment cannot be modified or deleted in its current state.
	ErrConflict = errors.New("execution environment conflict")
)

// EnvironmentConfigView holds the public union of pi-env and e2b configuration fields.
type EnvironmentConfigView struct {
	Host           string `json:"host,omitempty"`
	User           string `json:"user,omitempty"`
	Port           *int   `json:"port,omitempty"`
	IdentityFile   string `json:"identityFile,omitempty"`
	WorkspaceDir   string `json:"workspaceDir,omitempty"`
	APIURL         string `json:"apiUrl,omitempty"`
	Domain         string `json:"domain,omitempty"`
	EnvdURL        string `json:"envdUrl,omitempty"`
	Template       string `json:"template,omitempty"`
	TimeoutSeconds *int   `json:"timeoutSeconds,omitempty"`
	AutoPause      *bool  `json:"autoPause,omitempty"`
}

// Environment is the public view of a saved execution environment without its write-only secret.
type Environment struct {
	ID        string                `json:"id"`
	Name      string                `json:"name"`
	Kind      execenv.Kind          `json:"kind"`
	Config    EnvironmentConfigView `json:"config"`
	SecretSet bool                  `json:"secretSet"`
	CreatedAt int64                 `json:"createdAt"`
	UpdatedAt int64                 `json:"updatedAt"`
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
	name, kind, configJSON, err := s.prepareEnvironmentInput(ctx, id, in, in.Secret)
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
	name, kind, configJSON, err := s.prepareEnvironmentInput(ctx, id, in, secret)
	if err != nil {
		return Environment{}, err
	}
	if kind == execenv.KindPiEnv && s.dataDir != "" {
		oldCfg, _ := execenv.ParsePiEnvConfig(saved.Config)
		newCfg, _ := execenv.ParsePiEnvConfig(configJSON)
		if !samePiEnvEndpoint(oldCfg, newCfg) {
			_ = execenv.ForgetPiEnvHostKey(s.dataDir, id)
		}
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
	if saved.Kind == string(execenv.KindPiEnv) && s.dataDir != "" {
		_ = execenv.ForgetPiEnvHostKey(s.dataDir, id)
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
	res, err := s.envs.TestEnvironment(ctx, execenv.SpecFromRow(row))
	if errors.Is(err, execenv.ErrInvalidConfig) {
		return execenv.TestResult{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return res, err
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
	secret := strings.TrimSpace(in.Secret)
	var saved sqlc.ExecutionEnvironment
	if id != "" {
		var err error
		saved, err = s.q.GetEnvironment(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return execenv.TestResult{}, ErrNotFound
		}
		if err != nil {
			return execenv.TestResult{}, err
		}
		if secret == "" && saved.Kind == string(execenv.KindE2B) {
			secret = saved.Secret
		}
	}
	kind, rawCfg, err := s.validateKindAndConfig(ctx, id, in.Kind, in.Config, secret)
	if err != nil {
		return execenv.TestResult{}, err
	}
	ephemeral := true
	probeID := ""
	if id != "" && kind == execenv.KindPiEnv && saved.Kind == string(execenv.KindPiEnv) {
		oldCfg, _ := execenv.ParsePiEnvConfig(saved.Config)
		newCfg, _ := execenv.ParsePiEnvConfig(rawCfg)
		if samePiEnvEndpoint(oldCfg, newCfg) {
			ephemeral = false
			probeID = id
		}
	}
	res, err := s.envs.ProbeEnvironment(ctx, execenv.EnvironmentSpec{
		ID:     probeID,
		Kind:   kind,
		Config: rawCfg,
		Secret: secret,
	}, ephemeral)
	if errors.Is(err, execenv.ErrInvalidConfig) {
		return execenv.TestResult{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return res, err
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
	if execenv.Kind(saved.Kind) != execenv.KindPiEnv {
		return fmt.Errorf("%w: host key reset applies only to pi-env ssh environments", ErrInvalid)
	}
	if s.dataDir == "" {
		return nil
	}
	return execenv.ForgetPiEnvHostKey(s.dataDir, trimmedID)
}

func (s *Service) prepareEnvironmentInput(ctx context.Context, envID string, in EnvironmentInput, secret string) (string, execenv.Kind, string, error) {
	name := strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 60 {
		return "", "", "", fmt.Errorf("%w: name must be 1 to 60 characters", ErrInvalid)
	}
	kind, rawCfg, err := s.validateKindAndConfig(ctx, envID, in.Kind, in.Config, secret)
	if err != nil {
		return "", "", "", err
	}
	return name, kind, rawCfg, nil
}

func (s *Service) validateKindAndConfig(ctx context.Context, envID string, rawKind string, rawConfig []byte, secret string) (execenv.Kind, string, error) {
	kind := execenv.Kind(strings.TrimSpace(rawKind))
	rawCfg := strings.TrimSpace(string(rawConfig))
	if rawCfg == "" || rawCfg == "null" {
		rawCfg = "{}"
	}
	switch kind {
	case execenv.KindPiEnv:
		cfg, err := execenv.ParsePiEnvConfig(rawCfg)
		if err != nil {
			return "", "", fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		encoded, err := json.Marshal(cfg)
		if err != nil {
			return "", "", err
		}
		rawCfg = string(encoded)
	case execenv.KindE2B:
		cfg, err := execenv.ParseE2BConfig(rawCfg)
		if err != nil {
			return "", "", fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		if err := execenv.ValidateE2BConfig(cfg); err != nil {
			return "", "", fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		if !cfg.IsCustomEndpoint() && strings.TrimSpace(secret) == "" {
			return "", "", fmt.Errorf("%w: e2b api key is required", ErrInvalid)
		}
		encoded, err := json.Marshal(cfg)
		if err != nil {
			return "", "", err
		}
		rawCfg = string(encoded)
	case execenv.KindLocal:
		return "", "", fmt.Errorf("%w: unsupported environment kind %q", ErrInvalid, kind)
	default:
		return "", "", fmt.Errorf("%w: unsupported environment kind %q", ErrInvalid, kind)
	}
	if s.envs != nil {
		err := s.envs.ValidateEnvironment(ctx, execenv.EnvironmentSpec{
			ID:     envID,
			Kind:   kind,
			Config: rawCfg,
			Secret: strings.TrimSpace(secret),
		})
		if err != nil {
			return "", "", fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	}
	return kind, rawCfg, nil
}

func samePiEnvEndpoint(a, b execenv.PiEnvConfig) bool {
	var portA, portB int
	if a.Port != nil {
		portA = *a.Port
	}
	if b.Port != nil {
		portB = *b.Port
	}
	return a.Host == b.Host && portA == portB
}

func environmentView(row sqlc.ExecutionEnvironment) (Environment, error) {
	kind := execenv.Kind(row.Kind)
	var cfgView EnvironmentConfigView
	switch kind {
	case execenv.KindPiEnv:
		cfg, err := execenv.ParsePiEnvConfig(row.Config)
		if err != nil {
			return Environment{}, err
		}
		cfgView = EnvironmentConfigView{
			Host:         cfg.Host,
			User:         cfg.User,
			Port:         cfg.Port,
			IdentityFile: cfg.IdentityFile,
			WorkspaceDir: cfg.WorkspaceDir,
		}
	case execenv.KindE2B:
		cfg, err := execenv.ParseE2BConfig(row.Config)
		if err != nil {
			return Environment{}, err
		}
		cfgView = EnvironmentConfigView{
			APIURL:         cfg.APIURL,
			Domain:         cfg.Domain,
			EnvdURL:        cfg.EnvdURL,
			WorkspaceDir:   cfg.WorkspaceDir,
			Template:       cfg.Template,
			TimeoutSeconds: cfg.TimeoutSeconds,
			AutoPause:      cfg.AutoPause,
		}
	case execenv.KindLocal:
	}
	return Environment{
		ID:        row.ID,
		Name:      row.Name,
		Kind:      kind,
		Config:    cfgView,
		SecretSet: row.Secret != "",
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}, nil
}
