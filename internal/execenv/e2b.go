package execenv

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	"github.com/recally-io/dex/internal/db/sqlc"
)

const (
	defaultE2BAPIURL         = "https://api.e2b.app"
	defaultE2BDomain         = "e2b.app"
	defaultE2BTemplate       = "base"
	defaultE2BWorkspaceDir   = "/home/user/workspace"
	defaultE2BTimeoutSeconds = 300
	maxE2BTimeoutSeconds     = 86_400
	e2bConnectCacheTTL       = 20 * time.Second
)

type e2bConfig struct {
	APIURL         string `json:"apiUrl,omitempty"`
	Domain         string `json:"domain,omitempty"`
	EnvdURL        string `json:"envdUrl,omitempty"`
	Template       string `json:"template,omitempty"`
	WorkspaceDir   string `json:"workspaceDir,omitempty"`
	TimeoutSeconds *int   `json:"timeoutSeconds,omitempty"`
	AutoPause      *bool  `json:"autoPause,omitempty"`
}

func (c e2bConfig) isCustomEndpoint() bool {
	return c.APIURL != "" || c.Domain != "" || c.EnvdURL != ""
}

func (c e2bConfig) autoPauseEnabled() bool {
	return c.AutoPause == nil || *c.AutoPause
}

func parseE2BConfig(raw string) (e2bConfig, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		trimmed = "{}"
	}
	var cfg e2bConfig
	if err := json.Unmarshal([]byte(trimmed), &cfg); err != nil {
		return e2bConfig{}, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	cfg.APIURL = strings.TrimRight(strings.TrimSpace(cfg.APIURL), "/")
	cfg.Domain = strings.TrimRight(strings.TrimSpace(cfg.Domain), "/")
	cfg.EnvdURL = strings.TrimRight(strings.TrimSpace(cfg.EnvdURL), "/")
	cfg.Template = cmp.Or(strings.TrimSpace(cfg.Template), defaultE2BTemplate)
	cfg.WorkspaceDir = cmp.Or(strings.TrimSpace(cfg.WorkspaceDir), defaultE2BWorkspaceDir)
	timeout := defaultE2BTimeoutSeconds
	if cfg.TimeoutSeconds != nil {
		timeout = *cfg.TimeoutSeconds
	}
	cfg.TimeoutSeconds = new(timeout)
	if cfg.AutoPause != nil {
		cfg.AutoPause = new(*cfg.AutoPause)
	}
	return cfg, nil
}

func validateE2BConfig(cfg e2bConfig) error {
	if cfg.APIURL != "" {
		if err := validateE2BURL("apiUrl", cfg.APIURL); err != nil {
			return err
		}
	}
	if cfg.Domain != "" {
		if strings.ContainsAny(cfg.Domain, "\x00\r\n \t/?#@") || strings.Contains(cfg.Domain, "://") ||
			strings.HasPrefix(cfg.Domain, ".") || strings.HasSuffix(cfg.Domain, ".") {
			return fmt.Errorf("%w: domain must be a hostname (without scheme or path)", ErrInvalidConfig)
		}
	}
	if cfg.EnvdURL != "" {
		if strings.ContainsAny(cfg.EnvdURL, "\x00\r\n") {
			return fmt.Errorf("%w: envdUrl contains invalid characters", ErrInvalidConfig)
		}
		expanded := expandEnvdTemplate(cfg.EnvdURL, "sbx", e2bEnvdPort)
		if err := validateE2BURL("envdUrl", expanded); err != nil {
			return err
		}
	}
	if cfg.Template == "" || strings.ContainsAny(cfg.Template, "\x00\r\n") {
		return fmt.Errorf("%w: template must not be empty or contain control characters", ErrInvalidConfig)
	}
	if strings.ContainsAny(cfg.WorkspaceDir, "\x00\r\n") {
		return fmt.Errorf("%w: workspaceDir contains invalid characters", ErrInvalidConfig)
	}
	if cfg.WorkspaceDir != "~" && !strings.HasPrefix(cfg.WorkspaceDir, "~/") && !strings.HasPrefix(cfg.WorkspaceDir, "/") {
		return fmt.Errorf("%w: workspaceDir must be an absolute path or start with ~", ErrInvalidConfig)
	}
	if cfg.TimeoutSeconds == nil || *cfg.TimeoutSeconds < 1 || *cfg.TimeoutSeconds > maxE2BTimeoutSeconds {
		return fmt.Errorf("%w: timeoutSeconds must be between 1 and %d", ErrInvalidConfig, maxE2BTimeoutSeconds)
	}
	return nil
}

func validateE2BURL(field, raw string) error {
	if strings.ContainsAny(raw, "\x00\r\n") {
		return fmt.Errorf("%w: %s contains invalid characters", ErrInvalidConfig, field)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %s is invalid: %w", ErrInvalidConfig, field, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: %s must use http or https", ErrInvalidConfig, field)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: %s must include a host", ErrInvalidConfig, field)
	}
	if u.User != nil {
		return fmt.Errorf("%w: %s must not embed credentials", ErrInvalidConfig, field)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: %s must not include query parameters or fragments", ErrInvalidConfig, field)
	}
	return nil
}

type cachedE2BSession struct {
	envID       string
	specKey     string
	sandboxID   string
	accessToken string
	envd        *envdClient
	home        string
	workspace   string
	timeoutSec  int
	connectedAt time.Time
}

type e2bRuntime struct {
	q          *sqlc.Queries
	apiURL     string
	envdURL    string
	httpClient *http.Client

	mu         sync.Mutex
	agentLocks map[string]*sync.Mutex
	sessions   map[string]cachedE2BSession
}

func newE2BRuntime(q *sqlc.Queries, apiURL, envdURL string) *e2bRuntime {
	return &e2bRuntime{
		q:          q,
		apiURL:     cmp.Or(strings.TrimSpace(apiURL), defaultE2BAPIURL),
		envdURL:    strings.TrimSpace(envdURL),
		httpClient: newE2BHTTPClient(),
		agentLocks: map[string]*sync.Mutex{},
		sessions:   map[string]cachedE2BSession{},
	}
}

func (r *e2bRuntime) normalize(spec EnvironmentSpec) (string, error) {
	cfg, err := r.validate(spec)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (r *e2bRuntime) probe(ctx context.Context, _ *EnvironmentSpec, spec EnvironmentSpec) (TestResult, error) {
	cfg, err := r.validate(spec)
	if err != nil {
		return TestResult{}, err
	}
	ctrl := r.control(cfg, spec.Secret)
	sb, err := ctrl.createSandbox(ctx, cfg.Template, 60, cfg.autoPauseEnabled(), map[string]string{"dex_probe": "true"})
	if err != nil {
		return TestResult{OK: false, Error: err.Error()}, nil
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = ctrl.killSandbox(cleanupCtx, sb.SandboxID)
	}()

	envd := ctrl.envdFor(sb)
	envs, err := envd.getEnvs(ctx)
	if err != nil {
		return TestResult{OK: false, Error: err.Error()}, nil
	}
	home := cmp.Or(strings.TrimSpace(envs["HOME"]), "/home/user")
	workspace := resolveE2BWorkspace(cfg.WorkspaceDir, home)
	if err := makeEnvdDirAll(ctx, envd, workspace); err != nil {
		return TestResult{OK: false, Home: home, Error: err.Error()}, nil
	}
	platform, arch := probeEnvdPlatformArch(ctx, envd)
	return TestResult{OK: true, Platform: platform, Arch: arch, Home: home}, nil
}

func (r *e2bRuntime) lockAgent(agentID string) func() {
	r.mu.Lock()
	mu := r.agentLocks[agentID]
	if mu == nil {
		mu = &sync.Mutex{}
		r.agentLocks[agentID] = mu
	}
	r.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}

func (r *e2bRuntime) control(cfg e2bConfig, apiKey string) *e2bControlClient {
	apiURL := r.apiURL
	switch {
	case cfg.APIURL != "":
		apiURL = cfg.APIURL
	case cfg.Domain != "":
		apiURL = "https://api." + cfg.Domain
	}
	return &e2bControlClient{
		apiURL:         apiURL,
		defaultEnvdURL: r.envdURL,
		cfg:            cfg,
		apiKey:         strings.TrimSpace(apiKey),
		httpClient:     r.httpClient,
	}
}

func (r *e2bRuntime) validate(spec EnvironmentSpec) (e2bConfig, error) {
	cfg, err := parseE2BConfig(spec.Config)
	if err != nil {
		return e2bConfig{}, err
	}
	if err := validateE2BConfig(cfg); err != nil {
		return e2bConfig{}, err
	}
	if !cfg.isCustomEndpoint() && strings.TrimSpace(spec.Secret) == "" {
		return e2bConfig{}, fmt.Errorf("%w: e2b api key is required", ErrInvalidConfig)
	}
	return cfg, nil
}

func reconnectSandbox(ctx context.Context, ctrl *e2bControlClient, sandboxID string, timeoutSec int, cachedToken string) (*e2bSandbox, error) {
	sb, err := ctrl.connectSandbox(ctx, sandboxID, timeoutSec)
	if err == nil {
		return sb, nil
	}
	if errors.Is(err, errE2BNotFound) {
		return nil, nil //nolint:nilerr // expired or removed sandbox triggers fresh creation.
	}
	if !errors.Is(err, errE2BUnsupported) {
		return nil, err
	}
	if tErr := ctrl.setTimeout(ctx, sandboxID, timeoutSec); errors.Is(tErr, errE2BNotFound) {
		return nil, nil //nolint:nilerr // expired sandbox triggers fresh creation.
	} else if tErr != nil && !errors.Is(tErr, errE2BUnsupported) {
		return nil, tErr
	}
	gotSB, gErr := ctrl.getSandbox(ctx, sandboxID)
	switch {
	case gErr == nil:
		return gotSB, nil
	case errors.Is(gErr, errE2BNotFound):
		return nil, nil //nolint:nilerr // missing sandbox triggers fresh creation.
	case errors.Is(gErr, errE2BUnsupported):
		return &e2bSandbox{SandboxID: sandboxID, EnvdAccessToken: cachedToken}, nil
	default:
		return nil, gErr
	}
}

// acquire returns the agent's live sandbox session. It reuses a recent session, resumes the stored
// sandbox, or creates a new sandbox and stores its ID.
func (r *e2bRuntime) acquire(ctx context.Context, agentID string, spec EnvironmentSpec) (cachedE2BSession, error) {
	cfg, err := r.validate(spec)
	if err != nil {
		return cachedE2BSession{}, err
	}
	timeoutSec := *cfg.TimeoutSeconds
	specKey := fmt.Sprintf("%s|%d|%s|%s", spec.ID, spec.UpdatedAt, spec.Config, spec.Secret)

	unlock := r.lockAgent(agentID)
	defer unlock()

	row, err := r.q.GetAgent(ctx, agentID)
	if err != nil {
		return cachedE2BSession{}, err
	}
	persistedID := ""
	if row.EnvironmentID == spec.ID {
		persistedID = strings.TrimSpace(row.EnvironmentSandboxID)
	}

	r.mu.Lock()
	cached, hasCached := r.sessions[agentID]
	r.mu.Unlock()
	if hasCached && cached.specKey == specKey && cached.sandboxID == persistedID && cached.sandboxID != "" && time.Since(cached.connectedAt) < e2bConnectCacheTTL {
		return cached, nil
	}

	ctrl := r.control(cfg, spec.Secret)
	var sb *e2bSandbox
	if persistedID != "" {
		cachedToken := ""
		if hasCached && cached.sandboxID == persistedID {
			cachedToken = cached.accessToken
		}
		sb, err = reconnectSandbox(ctx, ctrl, persistedID, timeoutSec, cachedToken)
		if err != nil {
			return cachedE2BSession{}, err
		}
	}
	if sb == nil {
		sb, err = ctrl.createSandbox(ctx, cfg.Template, timeoutSec, cfg.autoPauseEnabled(), map[string]string{
			"dex_agent_id":       agentID,
			"dex_environment_id": spec.ID,
		})
		if err != nil {
			return cachedE2BSession{}, err
		}
		if err := r.q.SetAgentEnvironmentSandboxID(ctx, sqlc.SetAgentEnvironmentSandboxIDParams{
			SandboxID:     sb.SandboxID,
			AgentID:       agentID,
			EnvironmentID: spec.ID,
		}); err != nil {
			return cachedE2BSession{}, err
		}
	}

	envd := ctrl.envdFor(sb)
	home := cached.home
	workspace := cached.workspace
	if !hasCached || cached.sandboxID != sb.SandboxID || workspace == "" {
		envs, envErr := envd.getEnvs(ctx)
		if envErr != nil {
			return cachedE2BSession{}, envErr
		}
		home = cmp.Or(strings.TrimSpace(envs["HOME"]), "/home/user")
		workspace = resolveE2BWorkspace(cfg.WorkspaceDir, home)
		if err := makeEnvdDirAll(ctx, envd, workspace); err != nil {
			return cachedE2BSession{}, err
		}
	}

	session := cachedE2BSession{
		envID:       spec.ID,
		specKey:     specKey,
		sandboxID:   sb.SandboxID,
		accessToken: sb.EnvdAccessToken,
		envd:        envd,
		home:        home,
		workspace:   workspace,
		timeoutSec:  timeoutSec,
		connectedAt: time.Now(),
	}
	r.mu.Lock()
	r.sessions[agentID] = session
	r.mu.Unlock()
	return session, nil
}

func (r *e2bRuntime) open(ctx context.Context, agentID string, spec EnvironmentSpec) (lease, error) {
	session, err := r.acquire(ctx, agentID, spec)
	if err != nil {
		return nil, err
	}
	return &e2bHandle{
		rt:         r,
		agentID:    agentID,
		spec:       spec,
		home:       session.home,
		workspace:  session.workspace,
		timeoutSec: session.timeoutSec,
	}, nil
}

// changed keeps no per-environment state: acquire compares the spec of each cached session.
func (*e2bRuntime) changed(context.Context, EnvironmentSpec, *EnvironmentSpec) error { return nil }

func (*e2bRuntime) disposable() bool { return true }

// generation is constant: the stored spec and sandbox ID describe the whole E2B state.
func (*e2bRuntime) generation(string) uint64 { return 0 }

// release kills the agent's sandbox in spec's environment. It clears the stored sandbox ID only
// when the row still names that sandbox, so a sandbox that a later open stored stays.
func (r *e2bRuntime) release(ctx context.Context, agentID string, spec EnvironmentSpec, sandboxID string) error {
	unlock := r.lockAgent(agentID)
	defer unlock()

	r.mu.Lock()
	if s, ok := r.sessions[agentID]; ok && s.envID == spec.ID && (sandboxID == "" || s.sandboxID == sandboxID) {
		sandboxID = s.sandboxID
		delete(r.sessions, agentID)
	}
	r.mu.Unlock()
	if sandboxID == "" {
		return nil
	}
	if err := r.q.ClearAgentEnvironmentSandboxID(ctx, sqlc.ClearAgentEnvironmentSandboxIDParams{AgentID: agentID, SandboxID: sandboxID}); err != nil {
		return err
	}
	cfg, err := r.validate(spec)
	if err != nil {
		return nil //nolint:nilerr // a config that cannot reach E2B has no sandbox to kill.
	}
	killCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return r.control(cfg, spec.Secret).killSandbox(killCtx, sandboxID)
}

func resolveE2BWorkspace(configured, home string) string {
	raw := cmp.Or(strings.TrimSpace(configured), defaultE2BWorkspaceDir)
	if raw == "~" {
		return path.Clean(home)
	}
	if rest, ok := strings.CutPrefix(raw, "~/"); ok {
		return path.Join(home, rest)
	}
	return path.Clean(raw)
}

type e2bHandle struct {
	rt         *e2bRuntime
	agentID    string
	spec       EnvironmentSpec
	home       string
	workspace  string
	timeoutSec int
}

func (h *e2bHandle) Kind() Kind                  { return KindE2B }
func (h *e2bHandle) Workspace() string           { return h.workspace }
func (h *e2bHandle) Home() string                { return h.home }
func (h *e2bHandle) Close(context.Context) error { return nil }

func (h *e2bHandle) Env(ctx context.Context, cwd string, extraEnv map[string]string) (durableenv.ExecutionEnv, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolvedCwd := h.workspace
	if trimmed := strings.TrimSpace(cwd); trimmed != "" {
		resolvedCwd = resolveE2BTarget(h.home, h.workspace, trimmed)
	}
	return &e2bExecEnv{
		id:         "e2b:" + h.agentID,
		cwd:        resolvedCwd,
		home:       h.home,
		timeoutSec: h.timeoutSec,
		shellEnv:   maps.Clone(extraEnv),
		handle:     h,
	}, nil
}
