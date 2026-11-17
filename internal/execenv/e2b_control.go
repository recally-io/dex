package execenv

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	errE2BNotFound    = errors.New("e2b resource not found")
	errE2BUnsupported = errors.New("e2b endpoint unsupported")
)

type e2bSandbox struct {
	SandboxID       string  `json:"sandboxID"`
	EnvdAccessToken string  `json:"envdAccessToken,omitempty"`
	Domain          *string `json:"domain,omitempty"`
}

type e2bControlClient struct {
	apiURL         string
	defaultEnvdURL string
	cfg            E2BConfig
	apiKey         string
	httpClient     *http.Client
}

func newE2BHTTPClient() *http.Client {
	var transport *http.Transport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	} else {
		transport = &http.Transport{}
	}
	baseDialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err == nil && isLocalhostSubdomain(host) {
			addr = net.JoinHostPort("127.0.0.1", port)
		}
		return baseDialer.DialContext(ctx, network, addr)
	}
	return &http.Client{Transport: transport}
}

func isLocalhostSubdomain(host string) bool {
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	return strings.HasSuffix(lower, ".localhost") && len(lower) > len(".localhost")
}

func expandEnvdTemplate(raw, sandboxID string, port int) string {
	r := strings.NewReplacer("{sandboxId}", sandboxID, "{port}", strconv.Itoa(port))
	return strings.TrimRight(r.Replace(raw), "/")
}

func (c *e2bControlClient) createSandbox(ctx context.Context, template string, timeoutSec int, autoPause bool, meta map[string]string) (*e2bSandbox, error) {
	req := struct {
		TemplateID string            `json:"templateID"`
		Timeout    int               `json:"timeout"`
		AutoPause  bool              `json:"autoPause,omitzero"`
		AutoResume map[string]bool   `json:"autoResume,omitempty"`
		Metadata   map[string]string `json:"metadata,omitempty"`
	}{
		TemplateID: template,
		Timeout:    timeoutSec,
		Metadata:   meta,
	}
	if autoPause {
		req.AutoPause = true
		req.AutoResume = map[string]bool{"enabled": true}
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

func (c *e2bControlClient) getSandbox(ctx context.Context, sandboxID string) (*e2bSandbox, error) {
	var out e2bSandbox
	if err := c.doJSON(ctx, http.MethodGet, "/sandboxes/"+url.PathEscape(sandboxID), nil, &out); err != nil {
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
	baseURL := ""
	switch {
	case c.cfg.EnvdURL != "":
		baseURL = expandEnvdTemplate(c.cfg.EnvdURL, sb.SandboxID, e2bEnvdPort)
	case !c.cfg.IsCustomEndpoint() && strings.TrimSpace(c.defaultEnvdURL) != "":
		baseURL = strings.TrimRight(c.defaultEnvdURL, "/")
	default:
		baseURL = c.resolveSubdomainEnvdURL(sb)
	}
	return &envdClient{
		baseURL:     baseURL,
		sandboxID:   sb.SandboxID,
		accessToken: sb.EnvdAccessToken,
		httpClient:  c.httpClient,
	}
}

func (c *e2bControlClient) resolveSubdomainEnvdURL(sb *e2bSandbox) string {
	scheme := "https"
	if strings.HasPrefix(strings.ToLower(c.apiURL), "http://") {
		scheme = "http"
	}
	domain := ""
	switch {
	case sb.Domain != nil && strings.TrimSpace(*sb.Domain) != "":
		domain = strings.TrimSpace(*sb.Domain)
	case c.cfg.Domain != "":
		domain = c.cfg.Domain
	case c.cfg.APIURL != "":
		if u, err := url.Parse(c.cfg.APIURL); err == nil && u.Host != "" {
			hostname := u.Hostname()
			if net.ParseIP(hostname) != nil {
				return strings.TrimRight(c.cfg.APIURL, "/")
			}
			if len(hostname) > 4 && strings.EqualFold(hostname[:4], "api.") {
				domain = u.Host[4:]
			} else {
				domain = u.Host
			}
		}
	default:
		domain = defaultE2BDomain
	}
	if domain == "" {
		domain = defaultE2BDomain
	}
	return fmt.Sprintf("%s://%d-%s.%s", scheme, e2bEnvdPort, sb.SandboxID, domain)
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
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
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
	switch resp.StatusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", errE2BNotFound, msg)
	case http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return fmt.Errorf("%w: %s", errE2BUnsupported, msg)
	default:
		return fmt.Errorf("e2b api (%d): %s", resp.StatusCode, msg)
	}
}
