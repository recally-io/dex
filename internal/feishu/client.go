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
)

// Config holds credentials. APIURL is a test boundary; an empty URL selects Brand.
type Config struct {
	AppID     string
	AppSecret string
	OwnerID   string
	Brand     string
	APIURL    string
}

// Client owns the platform token cache and REST requests.
type Client struct {
	resourceTimeout time.Duration
	cfg             Config
	http            *http.Client
	mu              sync.Mutex
	token           string
	expires         time.Time
}

// NewClient creates a client without making a request.
func NewClient(cfg Config) *Client {
	if cfg.APIURL == "" {
		cfg.APIURL = "https://open.feishu.cn"
		if cfg.Brand == "lark" {
			cfg.APIURL = "https://open.larksuite.com"
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // dex does not replace the standard transport.
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &Client{cfg: cfg, http: &http.Client{Transport: transport}, resourceTimeout: 5 * time.Minute}
}

// APIError contains only codes, so a platform response cannot disclose credentials.
type APIError struct{ Status, Code int }

func (e *APIError) Error() string { return fmt.Sprintf("feishu HTTP %d, code %d", e.Status, e.Code) }

func (c *Client) tenantToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.expires) {
		return c.token, nil
	}
	data, err := c.request(ctx, http.MethodPost, "/open-apis/auth/v3/tenant_access_token/internal", map[string]string{"app_id": c.cfg.AppID, "app_secret": c.cfg.AppSecret}, "")
	if err != nil {
		return "", err
	}
	var result struct {
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", err
	}
	if result.Token == "" || result.Expire <= 0 {
		return "", errors.New("feishu returned no tenant token")
	}
	c.token = result.Token
	c.expires = time.Now().Add(time.Duration(max(0, result.Expire-60)) * time.Second)
	return c.token, nil
}

type response struct {
	data []byte
	body io.ReadCloser
}

func (c *Client) authorized(ctx context.Context, method, path string, body any, stream bool) (response, error) {
	token, err := c.tenantToken(ctx)
	if err != nil {
		return response{}, err
	}
	data, err := c.execute(ctx, method, path, body, token, stream)
	if e, ok := errors.AsType[*APIError](err); ok && (e.Code == 99991663 || e.Code == 99991668 || e.Code == 99991661) {
		c.mu.Lock()
		if c.token == token {
			c.expires = time.Time{}
		}
		c.mu.Unlock()
		token, err = c.tenantToken(ctx)
		if err != nil {
			return response{}, err
		}
		return c.execute(ctx, method, path, body, token, stream)
	}
	return data, err
}

func (c *Client) request(ctx context.Context, method, path string, body any, token string) ([]byte, error) {
	result, err := c.execute(ctx, method, path, body, token, false)
	return result.data, err
}

// execute owns failures and retries; only a successful resource transfers body ownership.
func (c *Client) execute(ctx context.Context, method, path string, body any, token string, stream bool) (response, error) {
	if !stream {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return response{}, err
		}
	}
	for attempt := range 3 {
		req, err := http.NewRequestWithContext(ctx, method, c.cfg.APIURL+path, bytes.NewReader(payload))
		if err != nil {
			return response{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := c.http.Do(req)
		retry := err != nil
		delay := retryDelay(attempt, "")
		var data []byte
		if err == nil {
			if stream && resp.StatusCode == http.StatusOK {
				return response{body: resp.Body}, nil
			}
			limit := int64(4 << 20)
			if stream {
				limit = 1 << 20
			}
			data, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
			_ = resp.Body.Close()
			if err == nil && int64(len(data)) > limit {
				return response{}, errors.New("feishu response exceeds size limit")
			}
			if err == nil {
				var envelope struct {
					Code int `json:"code"`
				}
				err = json.Unmarshal(data, &envelope)
				if resp.StatusCode < 200 || resp.StatusCode >= 300 || stream || (err == nil && envelope.Code != 0) {
					err = &APIError{resp.StatusCode, envelope.Code}
				}
				retry = retryable(resp.StatusCode, envelope.Code)
			} else {
				retry = true
			}
			delay = retryDelay(attempt, resp.Header.Get("Retry-After"))
		}
		if err == nil {
			return response{data: data}, nil
		}
		if !retry || attempt == 2 {
			return response{}, err
		}
		if err := pause(ctx, min(delay, 2*time.Second)); err != nil {
			return response{}, err
		}
	}
	panic("unreachable")
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
func (c *Client) Send(ctx context.Context, receiver, kind, content, deliveryID string) (string, error) {
	data, err := c.authorized(ctx, http.MethodPost, "/open-apis/im/v1/messages?receive_id_type=open_id", map[string]string{"receive_id": receiver, "msg_type": kind, "content": content, "uuid": deliveryID}, false)
	if err != nil {
		return "", err
	}
	var result struct {
		Data struct {
			ID string `json:"message_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data.data, &result); err != nil {
		return "", err
	}
	if result.Data.ID == "" {
		return "", errors.New("feishu returned no message ID")
	}
	return result.Data.ID, nil
}

// Patch replaces an interactive card.
func (c *Client) Patch(ctx context.Context, id, content string) error {
	_, err := c.authorized(ctx, http.MethodPatch, "/open-apis/im/v1/messages/"+url.PathEscape(id), map[string]string{"content": content}, false)
	return err
}

// Download streams one message resource to the caller. The caller closes the body.
func (c *Client) Download(ctx context.Context, messageID, key, kind string) (io.ReadCloser, error) {
	// Bound the full resource read, including error bodies, without the REST 30-second limit.
	ctx, cancel := context.WithTimeout(ctx, c.resourceTimeout)
	path := "/open-apis/im/v1/messages/" + url.PathEscape(messageID) + "/resources/" + url.PathEscape(key) + "?type=" + url.QueryEscape(kind)
	result, err := c.authorized(ctx, http.MethodGet, path, nil, true)
	if err != nil {
		cancel()
		return nil, err
	}
	return &resourceBody{ReadCloser: result.body, cancel: cancel}, nil
}

type resourceBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *resourceBody) Close() error {
	defer b.cancel()
	return b.ReadCloser.Close()
}

// TextContent encodes the platform's text content field.
func TextContent(text string) string {
	b, _ := json.Marshal(map[string]string{"text": text})
	return string(b)
}

// Parts splits UTF-8 text below the platform content limit.
func Parts(text string) []string {
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
