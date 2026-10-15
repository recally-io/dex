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
	cfg     Config
	http    *http.Client
	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewClient creates a client without making a request.
func NewClient(cfg Config) *Client {
	if cfg.APIURL == "" {
		cfg.APIURL = "https://open.feishu.cn"
		if cfg.Brand == "lark" {
			cfg.APIURL = "https://open.larksuite.com"
		}
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}
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

func (c *Client) authorized(ctx context.Context, method, path string, body any) ([]byte, error) {
	token, err := c.tenantToken(ctx)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, method, path, body, token)
	if e, ok := errors.AsType[*APIError](err); ok && (e.Code == 99991663 || e.Code == 99991668 || e.Code == 99991661) {
		c.mu.Lock()
		if c.token == token {
			c.expires = time.Time{}
		}
		c.mu.Unlock()
		token, err = c.tenantToken(ctx)
		if err != nil {
			return nil, err
		}
		return c.request(ctx, method, path, body, token)
	}
	return data, err
}

func (c *Client) request(ctx context.Context, method, path string, body any, token string) ([]byte, error) {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	for attempt := range 3 {
		req, err := http.NewRequestWithContext(ctx, method, c.cfg.APIURL+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
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
			data, err = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			_ = resp.Body.Close()
			if err == nil {
				var envelope struct {
					Code int `json:"code"`
				}
				err = json.Unmarshal(data, &envelope)
				if err == nil && (resp.StatusCode < 200 || resp.StatusCode >= 300 || envelope.Code != 0) {
					err = &APIError{resp.StatusCode, envelope.Code}
				}
				retry = retryable(resp.StatusCode, envelope.Code)
			} else {
				retry = true
			}
			delay = retryDelay(attempt, resp.Header.Get("Retry-After"))
		}
		if err == nil {
			return data, nil
		}
		if !retry || attempt == 2 {
			return nil, err
		}
		if err := pause(ctx, min(delay, 2*time.Second)); err != nil {
			return nil, err
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
	data, err := c.authorized(ctx, http.MethodPost, "/open-apis/im/v1/messages?receive_id_type=open_id", map[string]string{"receive_id": receiver, "msg_type": kind, "content": content, "uuid": deliveryID})
	if err != nil {
		return "", err
	}
	var result struct {
		Data struct {
			ID string `json:"message_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", err
	}
	if result.Data.ID == "" {
		return "", errors.New("feishu returned no message ID")
	}
	return result.Data.ID, nil
}

// Patch replaces an interactive card.
func (c *Client) Patch(ctx context.Context, id, content string) error {
	_, err := c.authorized(ctx, http.MethodPatch, "/open-apis/im/v1/messages/"+url.PathEscape(id), map[string]string{"content": content})
	return err
}

// Download streams one message resource to the caller. The caller closes the body.
func (c *Client) Download(ctx context.Context, messageID, key, kind string) (io.ReadCloser, error) {
	path := "/open-apis/im/v1/messages/" + url.PathEscape(messageID) + "/resources/" + url.PathEscape(key) + "?type=" + url.QueryEscape(kind)
	for attempt := range 3 {
		token, err := c.tenantToken(ctx)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIURL+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := c.http.Do(req)
		delay := retryDelay(attempt, "")
		retry := err != nil
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				return resp.Body, nil
			}
			var envelope struct {
				Code int `json:"code"`
			}
			decodeErr := json.UnmarshalRead(io.LimitReader(resp.Body, 1<<20), &envelope)
			_ = resp.Body.Close()
			err = &APIError{Status: resp.StatusCode, Code: envelope.Code}
			retry = retryable(resp.StatusCode, envelope.Code)
			if decodeErr == nil && (envelope.Code == 99991663 || envelope.Code == 99991668 || envelope.Code == 99991661) {
				c.mu.Lock()
				if c.token == token {
					c.expires = time.Time{}
				}
				c.mu.Unlock()
				retry = true
			}
			delay = retryDelay(attempt, resp.Header.Get("Retry-After"))
		}
		if !retry || attempt == 2 {
			return nil, err
		}
		if err := pause(ctx, min(delay, 2*time.Second)); err != nil {
			return nil, err
		}
	}
	panic("unreachable")
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
