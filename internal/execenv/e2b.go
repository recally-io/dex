package execenv

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
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
	e2bEnvdPort              = 49983
	e2bConnectCacheTTL       = 20 * time.Second
	maxConnectFrameBytes     = 16 * 1024 * 1024
)

var errE2BNotFound = errors.New("e2b resource not found")

// E2BConfig holds stored JSON settings for an E2B cloud sandbox environment.
type E2BConfig struct {
	Template       string `json:"template,omitempty"`
	WorkspaceDir   string `json:"workspaceDir,omitempty"`
	TimeoutSeconds *int   `json:"timeoutSeconds,omitempty"`
}

// ParseE2BConfig decodes and normalizes raw JSON configuration for e2b.
func ParseE2BConfig(raw string) (E2BConfig, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		trimmed = "{}"
	}
	var cfg E2BConfig
	if err := json.Unmarshal([]byte(trimmed), &cfg); err != nil {
		return E2BConfig{}, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	cfg.Template = cmp.Or(strings.TrimSpace(cfg.Template), defaultE2BTemplate)
	cfg.WorkspaceDir = cmp.Or(strings.TrimSpace(cfg.WorkspaceDir), defaultE2BWorkspaceDir)
	timeout := defaultE2BTimeoutSeconds
	if cfg.TimeoutSeconds != nil {
		timeout = *cfg.TimeoutSeconds
	}
	cfg.TimeoutSeconds = &timeout
	return cfg, nil
}

// ValidateE2BConfig validates an E2B configuration.
func ValidateE2BConfig(cfg E2BConfig) error {
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

type e2bSandbox struct {
	SandboxID       string  `json:"sandboxID"`
	EnvdAccessToken string  `json:"envdAccessToken,omitempty"`
	Domain          *string `json:"domain,omitempty"`
}

type e2bControlClient struct {
	apiURL     string
	envdURL    string
	apiKey     string
	httpClient *http.Client
}

func (c *e2bControlClient) createSandbox(ctx context.Context, template string, timeoutSec int, meta map[string]string) (*e2bSandbox, error) {
	req := struct {
		TemplateID string            `json:"templateID"`
		Timeout    int               `json:"timeout"`
		AutoPause  bool              `json:"autoPause"`
		AutoResume map[string]bool   `json:"autoResume"`
		Metadata   map[string]string `json:"metadata,omitempty"`
	}{
		TemplateID: template,
		Timeout:    timeoutSec,
		AutoPause:  true,
		AutoResume: map[string]bool{"enabled": true},
		Metadata:   meta,
	}
	var out e2bSandbox
	if err := c.doJSON(ctx, http.MethodPost, "/v2/sandboxes", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *e2bControlClient) connectSandbox(ctx context.Context, sandboxID string, timeoutSec int) (*e2bSandbox, error) {
	req := struct {
		Timeout int `json:"timeout"`
	}{Timeout: timeoutSec}
	var out e2bSandbox
	if err := c.doJSON(ctx, http.MethodPost, "/v2/sandboxes/"+url.PathEscape(sandboxID)+"/connect", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *e2bControlClient) setTimeout(ctx context.Context, sandboxID string, timeoutSec int) error {
	req := struct {
		Timeout int `json:"timeout"`
	}{Timeout: timeoutSec}
	return c.doJSON(ctx, http.MethodPost, "/sandboxes/"+url.PathEscape(sandboxID)+"/timeout", req, nil)
}

func (c *e2bControlClient) killSandbox(ctx context.Context, sandboxID string) error {
	err := c.doJSON(ctx, http.MethodDelete, "/sandboxes/"+url.PathEscape(sandboxID), nil, nil)
	if errors.Is(err, errE2BNotFound) {
		return nil
	}
	return err
}

func (c *e2bControlClient) envdFor(sb *e2bSandbox) *envdClient {
	baseURL := strings.TrimRight(c.envdURL, "/")
	if baseURL == "" {
		domain := defaultE2BDomain
		if sb.Domain != nil && strings.TrimSpace(*sb.Domain) != "" {
			domain = strings.TrimSpace(*sb.Domain)
		}
		baseURL = fmt.Sprintf("https://%d-%s.%s", e2bEnvdPort, sb.SandboxID, domain)
	}
	return &envdClient{
		baseURL:     baseURL,
		sandboxID:   sb.SandboxID,
		accessToken: sb.EnvdAccessToken,
		httpClient:  c.httpClient,
	}
}

func (c *e2bControlClient) doJSON(ctx context.Context, method, reqPath string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.apiURL, "/")+reqPath, reader)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil && len(bytes.TrimSpace(raw)) > 0 {
			return json.Unmarshal(raw, out)
		}
		return nil
	}
	var apiErr struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &apiErr)
	msg := cmp.Or(strings.TrimSpace(apiErr.Message), strings.TrimSpace(string(raw)), resp.Status)
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s", errE2BNotFound, msg)
	}
	return fmt.Errorf("e2b api (%d): %s", resp.StatusCode, msg)
}

type protoInt64 int64

func (v *protoInt64) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() == '"' {
		n, err := strconv.ParseInt(tok.String(), 10, 64)
		*v = protoInt64(n)
		return err
	}
	if tok.Kind() == '0' {
		n, err := tok.Int()
		*v = protoInt64(n)
		return err
	}
	return fmt.Errorf("unexpected proto int64 token %v", tok.Kind())
}

type envdConnectError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *envdConnectError) Error() string { return e.Code + ": " + e.Message }

type envdEntry struct {
	Name          string     `json:"name"`
	Type          string     `json:"type"`
	Path          string     `json:"path"`
	Size          protoInt64 `json:"size"`
	ModifiedTime  *time.Time `json:"modifiedTime,omitempty"`
	SymlinkTarget *string    `json:"symlinkTarget,omitempty"`
}

type envdProcessEvent struct {
	Start *struct {
		PID uint32 `json:"pid"`
	} `json:"start,omitempty"`
	Data *struct {
		Stdout []byte `json:"stdout,omitempty"`
		Stderr []byte `json:"stderr,omitempty"`
	} `json:"data,omitempty"`
	End *struct {
		ExitCode int     `json:"exitCode"`
		Exited   bool    `json:"exited"`
		Error    *string `json:"error,omitempty"`
	} `json:"end,omitempty"`
	Keepalive *struct{} `json:"keepalive,omitempty"`
}

type envdClient struct {
	baseURL     string
	sandboxID   string
	accessToken string
	httpClient  *http.Client
}

func (c *envdClient) applyHeaders(req *http.Request) {
	if c.accessToken != "" {
		req.Header.Set("X-Access-Token", c.accessToken)
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("user:")))
	if c.sandboxID != "" {
		req.Header.Set("E2b-Sandbox-Id", c.sandboxID)
		req.Header.Set("E2b-Sandbox-Port", strconv.Itoa(e2bEnvdPort))
	}
}

func (c *envdClient) filesURL(filePath string) string {
	q := url.Values{}
	q.Set("path", filePath)
	q.Set("username", "user")
	return c.baseURL + "/files?" + q.Encode()
}

func (c *envdClient) getEnvs(ctx context.Context) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/envs", nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseEnvdHTTPError(resp)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c *envdClient) downloadFile(ctx context.Context, filePath string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.filesURL(filePath), nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseEnvdHTTPError(resp)
	}
	return io.ReadAll(resp.Body)
}

func (c *envdClient) downloadRange(ctx context.Context, filePath string, offset, length int64) ([]byte, error) {
	if length <= 0 {
		return []byte{}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.filesURL(filePath), nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return []byte{}, nil
	}
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return nil, parseEnvdHTTPError(resp)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		if offset >= int64(len(data)) {
			return []byte{}, nil
		}
		data = data[offset:min(int64(len(data)), offset+length)]
	}
	return data, nil
}

func (c *envdClient) uploadFile(ctx context.Context, filePath string, data []byte) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filePath)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.filesURL(filePath), &body)
	if err != nil {
		return err
	}
	c.applyHeaders(req)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseEnvdHTTPError(resp)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *envdClient) stat(ctx context.Context, filePath string) (*envdEntry, error) {
	var resp struct {
		Entry *envdEntry `json:"entry"`
	}
	if err := c.callUnary(ctx, "/filesystem.Filesystem/Stat", map[string]string{"path": filePath}, &resp); err != nil {
		return nil, err
	}
	if resp.Entry == nil {
		return nil, &envdConnectError{Code: "not_found", Message: "missing entry"}
	}
	return resp.Entry, nil
}

func (c *envdClient) makeDir(ctx context.Context, dirPath string) error {
	return c.callUnary(ctx, "/filesystem.Filesystem/MakeDir", map[string]string{"path": dirPath}, nil)
}

func (c *envdClient) listDir(ctx context.Context, dirPath string) ([]envdEntry, error) {
	var resp struct {
		Entries []envdEntry `json:"entries"`
	}
	err := c.callUnary(ctx, "/filesystem.Filesystem/ListDir", map[string]any{"path": dirPath, "depth": 1}, &resp)
	return resp.Entries, err
}

func (c *envdClient) remove(ctx context.Context, targetPath string) error {
	return c.callUnary(ctx, "/filesystem.Filesystem/Remove", map[string]string{"path": targetPath}, nil)
}

func (c *envdClient) sendSignal(ctx context.Context, pid uint32, sig string) error {
	return c.callUnary(ctx, "/process.Process/SendSignal", map[string]any{
		"process": map[string]uint32{"pid": pid},
		"signal":  sig,
	}, nil)
}

func (c *envdClient) startProcess(ctx context.Context, procCfg map[string]any, onEvent func(envdProcessEvent) error) error {
	encoded, err := json.Marshal(map[string]any{"process": procCfg, "stdin": false})
	if err != nil {
		return err
	}
	frame := make([]byte, 5+len(encoded))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(encoded))) //nolint:gosec // bounded RPC payload.
	copy(frame[5:], encoded)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/process.Process/Start", bytes.NewReader(frame))
	if err != nil {
		return err
	}
	c.applyHeaders(req)
	req.Header.Set("Content-Type", "application/connect+json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Keepalive-Ping-Interval", "50")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/connect+") {
		return parseEnvdHTTPError(resp)
	}

	for {
		var hdr [5]byte
		n, err := io.ReadFull(resp.Body, hdr[:])
		if n == 0 && errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		flags := hdr[0]
		length := binary.BigEndian.Uint32(hdr[1:5])
		if length > maxConnectFrameBytes {
			return fmt.Errorf("envd frame %d exceeds limit", length)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(resp.Body, payload); err != nil {
			return err
		}
		if flags&0x01 != 0 {
			zr, gzErr := gzip.NewReader(bytes.NewReader(payload))
			if gzErr != nil {
				return gzErr
			}
			payload, err = io.ReadAll(io.LimitReader(zr, maxConnectFrameBytes))
			_ = zr.Close()
			if err != nil {
				return err
			}
		}
		if flags&0x02 != 0 {
			var endFrame struct {
				Error *envdConnectError `json:"error,omitempty"`
			}
			if len(bytes.TrimSpace(payload)) > 0 {
				_ = json.Unmarshal(payload, &endFrame)
			}
			if endFrame.Error != nil {
				return endFrame.Error
			}
			return nil
		}
		var msg struct {
			Event *envdProcessEvent `json:"event,omitempty"`
		}
		if err := json.Unmarshal(payload, &msg); err != nil {
			return err
		}
		if msg.Event != nil && msg.Event.Keepalive == nil {
			if err := onEvent(*msg.Event); err != nil {
				return err
			}
		}
	}
}

func (c *envdClient) callUnary(ctx context.Context, procedure string, reqBody any, respOut any) error {
	encoded, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+procedure, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	c.applyHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return parseEnvdHTTPError(resp)
	}
	if respOut == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return err
	}
	return json.Unmarshal(raw, respOut)
}

func parseEnvdHTTPError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var ce envdConnectError
	if json.Unmarshal(raw, &ce) == nil && ce.Code != "" {
		return &ce
	}
	code := "unknown"
	switch resp.StatusCode {
	case http.StatusNotFound:
		code = "not_found"
	case http.StatusForbidden, http.StatusUnauthorized:
		code = "permission_denied"
	case http.StatusBadRequest:
		code = "invalid_argument"
	case http.StatusConflict:
		code = "already_exists"
	}
	return &envdConnectError{Code: code, Message: cmp.Or(strings.TrimSpace(string(raw)), resp.Status)}
}

type cachedE2BSession struct {
	envID       string
	specKey     string
	sandboxID   string
	envd        *envdClient
	home        string
	workspace   string
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

func newE2BRuntime(q *sqlc.Queries, apiURL, envdURL string, httpClient *http.Client) *e2bRuntime {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &e2bRuntime{
		q:          q,
		apiURL:     cmp.Or(strings.TrimSpace(apiURL), defaultE2BAPIURL),
		envdURL:    strings.TrimSpace(envdURL),
		httpClient: httpClient,
		agentLocks: map[string]*sync.Mutex{},
		sessions:   map[string]cachedE2BSession{},
	}
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

func (r *e2bRuntime) control(apiKey string) *e2bControlClient {
	return &e2bControlClient{
		apiURL:     r.apiURL,
		envdURL:    r.envdURL,
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: r.httpClient,
	}
}

func (r *e2bRuntime) validate(spec EnvironmentSpec) (E2BConfig, error) {
	if strings.TrimSpace(spec.Secret) == "" {
		return E2BConfig{}, fmt.Errorf("%w: e2b api key is required", ErrInvalidConfig)
	}
	cfg, err := ParseE2BConfig(spec.Config)
	if err != nil {
		return E2BConfig{}, err
	}
	return cfg, ValidateE2BConfig(cfg)
}

func (r *e2bRuntime) acquire(ctx context.Context, agentID string, spec EnvironmentSpec) (*envdClient, string, string, string, int, error) {
	cfg, err := r.validate(spec)
	if err != nil {
		return nil, "", "", "", 0, err
	}
	timeoutSec := *cfg.TimeoutSeconds
	specKey := fmt.Sprintf("%s|%d|%s|%s", spec.ID, spec.UpdatedAt, spec.Config, spec.Secret)

	unlock := r.lockAgent(agentID)
	defer unlock()

	persistedID := ""
	if r.q != nil {
		row, dbErr := r.q.GetAgent(ctx, agentID)
		if dbErr != nil {
			return nil, "", "", "", 0, dbErr
		}
		if row.EnvironmentID == spec.ID {
			persistedID = strings.TrimSpace(row.EnvironmentSandboxID)
		}
	}

	r.mu.Lock()
	cached, hasCached := r.sessions[agentID]
	r.mu.Unlock()
	if hasCached && cached.specKey == specKey && (r.q == nil || cached.sandboxID == persistedID) && cached.sandboxID != "" && time.Since(cached.connectedAt) < e2bConnectCacheTTL {
		return cached.envd, cached.home, cached.workspace, cached.sandboxID, timeoutSec, nil
	}
	if persistedID == "" && r.q == nil && hasCached && cached.specKey == specKey {
		persistedID = cached.sandboxID
	}

	ctrl := r.control(spec.Secret)
	var sb *e2bSandbox
	if persistedID != "" {
		sb, err = ctrl.connectSandbox(ctx, persistedID, timeoutSec)
		if errors.Is(err, errE2BNotFound) {
			sb = nil
		} else if err != nil {
			return nil, "", "", "", 0, err
		}
	}
	if sb == nil {
		sb, err = ctrl.createSandbox(ctx, cfg.Template, timeoutSec, map[string]string{
			"dex_agent_id":       agentID,
			"dex_environment_id": spec.ID,
		})
		if err != nil {
			return nil, "", "", "", 0, err
		}
		if r.q != nil {
			if err := r.q.SetAgentEnvironmentSandboxID(ctx, sqlc.SetAgentEnvironmentSandboxIDParams{
				SandboxID:     sb.SandboxID,
				AgentID:       agentID,
				EnvironmentID: spec.ID,
			}); err != nil {
				return nil, "", "", "", 0, err
			}
		}
	}

	envd := ctrl.envdFor(sb)
	home := cached.home
	workspace := cached.workspace
	if !hasCached || cached.sandboxID != sb.SandboxID || workspace == "" {
		envs, envErr := envd.getEnvs(ctx)
		if envErr != nil {
			return nil, "", "", "", 0, envErr
		}
		home = cmp.Or(strings.TrimSpace(envs["HOME"]), "/home/user")
		workspace = resolveE2BWorkspace(cfg.WorkspaceDir, home)
		if err := makeEnvdDirAll(ctx, envd, workspace); err != nil {
			return nil, "", "", "", 0, err
		}
	}

	r.mu.Lock()
	r.sessions[agentID] = cachedE2BSession{
		envID:       spec.ID,
		specKey:     specKey,
		sandboxID:   sb.SandboxID,
		envd:        envd,
		home:        home,
		workspace:   workspace,
		connectedAt: time.Now(),
	}
	r.mu.Unlock()
	return envd, home, workspace, sb.SandboxID, timeoutSec, nil
}

func (r *e2bRuntime) open(ctx context.Context, agentID string, spec EnvironmentSpec) (Handle, error) {
	_, home, workspace, _, timeoutSec, err := r.acquire(ctx, agentID, spec)
	if err != nil {
		return nil, err
	}
	return &e2bHandle{
		rt:         r,
		agentID:    agentID,
		spec:       spec,
		home:       home,
		workspace:  workspace,
		timeoutSec: timeoutSec,
	}, nil
}

func (r *e2bRuntime) test(ctx context.Context, spec EnvironmentSpec) (TestResult, error) {
	cfg, err := r.validate(spec)
	if err != nil {
		return TestResult{}, err
	}
	ctrl := r.control(spec.Secret)
	sb, err := ctrl.createSandbox(ctx, cfg.Template, 60, map[string]string{"dex_probe": "true"})
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
	return TestResult{OK: true, Platform: "linux", Arch: "x64", Home: home}, nil
}

func (r *e2bRuntime) resetAgent(ctx context.Context, agentID string) error {
	unlock := r.lockAgent(agentID)
	defer unlock()

	r.mu.Lock()
	cached := r.sessions[agentID]
	delete(r.sessions, agentID)
	r.mu.Unlock()

	if r.q == nil {
		return nil
	}
	row, err := r.q.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}
	sandboxID := cmp.Or(strings.TrimSpace(row.EnvironmentSandboxID), cached.sandboxID)
	if err := r.q.ClearAgentEnvironmentSandboxID(ctx, agentID); err != nil {
		return err
	}
	if sandboxID == "" || row.EnvironmentID == "" {
		return nil
	}
	envRow, err := r.q.GetEnvironment(ctx, row.EnvironmentID)
	if err != nil || Kind(envRow.Kind) != KindE2B || strings.TrimSpace(envRow.Secret) == "" {
		return nil //nolint:nilerr // missing or non-e2b environment requires no remote kill.
	}
	return r.control(envRow.Secret).killSandbox(ctx, sandboxID)
}

func (r *e2bRuntime) cleanupSandbox(ctx context.Context, agentID, envID, sandboxID string) {
	r.mu.Lock()
	if s, ok := r.sessions[agentID]; ok && (sandboxID == "" || s.sandboxID == sandboxID) {
		if sandboxID == "" {
			sandboxID = s.sandboxID
		}
		delete(r.sessions, agentID)
	}
	r.mu.Unlock()
	if r.q == nil || envID == "" || sandboxID == "" {
		return
	}
	envRow, err := r.q.GetEnvironment(ctx, envID)
	if err != nil || Kind(envRow.Kind) != KindE2B || strings.TrimSpace(envRow.Secret) == "" {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_ = r.control(envRow.Secret).killSandbox(cleanupCtx, sandboxID)
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

func makeEnvdDirAll(ctx context.Context, envd *envdClient, dirPath string) error {
	cleaned := path.Clean(dirPath)
	if cleaned == "/" || cleaned == "." {
		return nil
	}
	var cur strings.Builder
	for seg := range strings.SplitSeq(strings.TrimPrefix(cleaned, "/"), "/") {
		if seg == "" {
			continue
		}
		cur.WriteByte('/')
		cur.WriteString(seg)
		dir := cur.String()
		if err := envd.makeDir(ctx, dir); err != nil {
			if ce, ok := errors.AsType[*envdConnectError](err); ok && ce.Code == "already_exists" {
				continue
			}
			if entry, statErr := envd.stat(ctx, dir); statErr == nil && entry.Type == "FILE_TYPE_DIRECTORY" {
				continue
			}
			return err
		}
	}
	return nil
}

type e2bHandle struct {
	rt         *e2bRuntime
	agentID    string
	spec       EnvironmentSpec
	home       string
	workspace  string
	timeoutSec int
}

func (h *e2bHandle) Kind() Kind                               { return KindE2B }
func (h *e2bHandle) ID() string                               { return h.agentID }
func (h *e2bHandle) Workspace() string                        { return h.workspace }
func (h *e2bHandle) Home(ctx context.Context) (string, error) { return h.home, ctx.Err() }
func (h *e2bHandle) Close(context.Context) error              { return nil }

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
		acquire: func(acqCtx context.Context) (*envdClient, string, error) {
			envd, _, _, sbID, _, err := h.rt.acquire(acqCtx, h.agentID, h.spec)
			return envd, sbID, err
		},
		renew: func(renCtx context.Context, sbID string) {
			_ = h.rt.control(h.spec.Secret).setTimeout(renCtx, sbID, h.timeoutSec)
		},
	}, nil
}
