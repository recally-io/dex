// Package feishu connects one owner's private messages through Feishu or Lark.
package feishu

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// Config holds credentials. APIURL is a test boundary; an empty URL selects Brand.
type Config struct {
	AppID     string
	AppSecret string
	OwnerID   string
	Brand     string
	APIURL    string
	Reactions *bool `json:"reactions,omitempty"`
}

const resourceTimeout = 5 * time.Minute

// client owns the platform token cache and REST requests.
type client struct {
	cfg     Config
	http    *http.Client
	sdk     *larkcore.Config
	im      *larkim.V1
	mu      sync.Mutex
	token   string
	expires time.Time
	once    bool
}

// newClient creates a client without making a request.
func newClient(cfg Config) *client {
	if cfg.APIURL == "" {
		cfg.APIURL = "https://open.feishu.cn"
		if cfg.Brand == "lark" {
			cfg.APIURL = "https://open.larksuite.com"
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // dex does not replace the standard transport.
	transport.ResponseHeaderTimeout = 30 * time.Second
	httpClient := &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		// A redirect must not replay a mutation or forward app credentials.
		if via[0].Method != http.MethodGet || len(via) >= 10 {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	sdk := &larkcore.Config{
		BaseUrl: cfg.APIURL, AppId: cfg.AppID, AppSecret: cfg.AppSecret,
		HttpClient: restHTTP{httpClient}, Logger: silentSDKLog{}, Serializable: sdkJSON{},
	}
	return &client{cfg: cfg, http: httpClient, sdk: sdk, im: larkim.New(sdk)}
}

// apiError contains only codes, so a platform response cannot disclose credentials.
type apiError struct{ Status, Code int }

func (e *apiError) Error() string { return fmt.Sprintf("feishu HTTP %d, code %d", e.Status, e.Code) }

func (c *client) tenantToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.expires) {
		return c.token, nil
	}
	data, err := c.request(ctx, larkcore.TenantAccessTokenInternalUrlPath, &larkcore.SelfBuiltTenantAccessTokenReq{
		AppID: c.cfg.AppID, AppSecret: c.cfg.AppSecret,
	})
	if err != nil {
		return "", err
	}
	var result larkcore.TenantAccessTokenResp
	if err := json.Unmarshal(data, &result); err != nil || result.TenantAccessToken == "" || result.Expire <= 0 {
		return "", errors.New("feishu returned an invalid tenant token")
	}
	c.token = result.TenantAccessToken
	c.expires = time.Now().Add(time.Duration(max(0, result.Expire-60)) * time.Second)
	return c.token, nil
}

// SDK caching is global and does not evict the rejected token before replaying.
// Keep credentials scoped to the current client, including the reaction lane.
func authorized[T any](ctx context.Context, c *client, call func(string) (T, error)) (T, error) {
	token, err := c.tenantToken(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	result, err := call(token)
	if e, ok := errors.AsType[*apiError](err); ok && (e.Code == 99991663 || e.Code == 99991668 || e.Code == 99991661) {
		c.mu.Lock()
		if c.token == token {
			c.expires = time.Time{}
		}
		c.mu.Unlock()
		token, err = c.tenantToken(ctx)
		if err != nil {
			var zero T
			return zero, err
		}
		return call(token)
	}
	return result, err
}

func (c *client) request(ctx context.Context, path string, body any) ([]byte, error) {
	resp, err := rest(ctx, c, func(ctx context.Context) (*larkcore.ApiResp, error) {
		return larkcore.Request(ctx, &larkcore.ApiReq{HttpMethod: http.MethodPost, ApiPath: path, Body: body}, c.sdk)
	})
	if err != nil {
		return nil, err
	}
	return resp.RawBody, nil
}

func rest[T any](ctx context.Context, c *client, call func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return retry(ctx, c, call)
}

func retry[T any](ctx context.Context, c *client, call func(context.Context) (T, error)) (T, error) {
	attempts := 3
	if c.once {
		attempts = 1
	}
	for attempt := range attempts {
		result, err := call(ctx)
		failure, transient := errors.AsType[*retryError](err)
		if !transient {
			return result, err
		}
		if attempt == attempts-1 {
			return result, failure.error
		}
		if err := pause(ctx, retryDelay(attempt, failure.after)); err != nil {
			return result, err
		}
	}
	panic("unreachable")
}

// restHTTP preserves Dex's status and size checks before SDK decoding. Errors
// have no response text and are never *url.Error, which the SDK can retry.
// Only successful envelopes reach the SDK, so its app ticket and token refresh
// paths cannot replay a mutation. Higher layers decide whether to retry.
type restHTTP struct{ http *http.Client }

func (h restHTTP) Do(req *http.Request) (*http.Response, error) {
	resp, err := h.http.Do(req) //nolint:gosec // SDK uses built-in domains (an override is test-only) and fixed paths with escaped parameters.
	if err != nil {
		return nil, transportFailure(req.Context())
	}
	data, err := readHTTP(resp, 4<<20, false)
	if err != nil {
		return nil, err
	}
	// The endpoint contract is JSON regardless of the platform's content type.
	resp.Header.Set("Content-Type", "application/json")
	resp.Body = io.NopCloser(bytes.NewReader(data))
	return resp, nil
}

type sdkJSON struct{}

func (sdkJSON) Serialize(v any) ([]byte, error) { return json.Marshal(v) }
func (sdkJSON) Deserialize(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return errors.New("feishu returned an invalid API response")
	}
	return nil
}

// Do not log SDK request/response details; they can contain credentials.
type silentSDKLog struct{}

func (silentSDKLog) Debug(context.Context, ...any) {}
func (silentSDKLog) Info(context.Context, ...any)  {}
func (silentSDKLog) Warn(context.Context, ...any)  {}
func (silentSDKLog) Error(context.Context, ...any) {}

type retryError struct {
	error
	after string
}

func transportFailure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return &retryError{error: errors.New("feishu HTTP request failed")}
}

func readHTTP(resp *http.Response, limit int64, resource bool) ([]byte, error) {
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	after := resp.Header.Get("Retry-After")
	if err != nil {
		return nil, &retryError{error: errors.New("feishu HTTP response failed"), after: after}
	}
	if int64(len(data)) > limit {
		return nil, errors.New("feishu response exceeds size limit")
	}
	var envelope struct {
		Code int `json:"code"`
	}
	err = json.Unmarshal(data, &envelope)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || resource || (err == nil && envelope.Code != 0) {
		err = &apiError{resp.StatusCode, envelope.Code}
	} else if err != nil {
		err = errors.New("feishu returned an invalid API response")
	}
	if retryable(resp.StatusCode, envelope.Code) {
		return nil, &retryError{error: err, after: after}
	}
	return data, err
}

func retryable(status, code int) bool {
	return status == http.StatusTooManyRequests || status >= 500 || (code >= 500 && code < 600) || code == 99991400 || code == 99991401 || code == 99991402 || code == 230020
}

func retryDelay(attempt int, retryAfter string) time.Duration {
	delay := time.Duration(200*(1<<uint(attempt*2))) * time.Millisecond
	if seconds, err := strconv.Atoi(retryAfter); err == nil {
		delay = max(delay, time.Duration(min(seconds, 2))*time.Second)
	} else if at, err := http.ParseTime(retryAfter); err == nil {
		delay = max(delay, time.Until(at))
	}
	return min(delay, 2*time.Second)
}

func pause(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Send creates a message. deliveryID stays the same across retries.
func (c *client) Send(ctx context.Context, receiver, kind, content, deliveryID string) (string, error) {
	resp, err := authorized(ctx, c, func(token string) (*larkim.CreateMessageResp, error) {
		req := larkim.NewCreateMessageReqBuilder().ReceiveIdType("open_id").Body(&larkim.CreateMessageReqBody{
			ReceiveId: new(receiver), MsgType: new(kind), Content: new(content), Uuid: new(deliveryID),
		}).Build()
		return rest(ctx, c, func(ctx context.Context) (*larkim.CreateMessageResp, error) {
			return c.im.Message.Create(ctx, req, larkcore.WithTenantAccessToken(token))
		})
	})
	if err != nil {
		return "", err
	}
	if resp.Data == nil || resp.Data.MessageId == nil || *resp.Data.MessageId == "" {
		return "", errors.New("feishu returned no message ID")
	}
	return *resp.Data.MessageId, nil
}

// Patch replaces an interactive card.
func (c *client) Patch(ctx context.Context, id, content string) error {
	_, err := authorized(ctx, c, func(token string) (*larkim.PatchMessageResp, error) {
		req := larkim.NewPatchMessageReqBuilder().MessageId(id).Body(&larkim.PatchMessageReqBody{Content: new(content)}).Build()
		return rest(ctx, c, func(ctx context.Context) (*larkim.PatchMessageResp, error) {
			return c.im.Message.Patch(ctx, req, larkcore.WithTenantAccessToken(token))
		})
	})
	return err
}

// Download streams one message resource to the caller. The caller closes the body.
func (c *client) Download(ctx context.Context, messageID, key, kind string) (io.ReadCloser, error) {
	// The SDK buffers file responses before returning them. Dex enforces the
	// upload limit while streaming and bounds the full resource read separately.
	ctx, cancel := context.WithTimeout(ctx, resourceTimeout)
	path := "/open-apis/im/v1/messages/" + url.PathEscape(messageID) + "/resources/" + url.PathEscape(key) + "?type=" + url.QueryEscape(kind)
	body, err := authorized(ctx, c, func(token string) (io.ReadCloser, error) {
		return retry(ctx, c, func(ctx context.Context) (io.ReadCloser, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIURL+path, nil)
			if err != nil {
				return nil, errors.New("feishu resource URL is invalid")
			}
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := c.http.Do(req)
			if err != nil {
				return nil, transportFailure(ctx)
			}
			if resp.StatusCode == http.StatusOK {
				return resp.Body, nil
			}
			_, err = readHTTP(resp, 1<<20, true)
			return nil, err
		})
	})
	if err != nil {
		cancel()
		return nil, err
	}
	return &resourceBody{ReadCloser: body, cancel: cancel}, nil
}

type resourceBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *resourceBody) Close() error {
	defer b.cancel()
	return b.ReadCloser.Close()
}

// textContent encodes the platform's text content field.
func textContent(text string) string {
	b, _ := json.Marshal(map[string]string{"text": text})
	return string(b)
}

// parts splits UTF-8 text below the platform content limit.
func parts(text string) []string {
	const limit = 4000
	var parts []string
	for len(text) > limit {
		end := limit
		for end > 0 && text[end]&0xc0 == 0x80 {
			end--
		}
		if line := strings.LastIndexByte(text[:end], '\n'); line > limit/2 {
			end = line + 1
		}
		parts = append(parts, text[:end])
		text = text[end:]
	}
	if text != "" {
		parts = append(parts, text)
	}
	return parts
}
