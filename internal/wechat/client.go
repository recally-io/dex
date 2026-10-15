// Package wechat connects the owner's private WeChat messages through the iLink Bot API.
// The channel manager owns commands and durable reply delivery. This package owns QR login,
// protocol requests, inbound media, and typing. It never opens an agent's worker storage.
package wechat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultAPI = "https://ilinkai.weixin.qq.com"
const defaultCDN = "https://novac2c.cdn.weixin.qq.com/c2c"
const channelVersion = "2.4.10" // iLink wire compatibility with the official client

// ErrSessionExpired means iLink rejected the saved login. Both ret and errcode can carry -14.
var ErrSessionExpired = errors.New("WeChat login expired; scan again")

// Options gives the protocol client its build version and optional local test endpoints.
type Options struct {
	APIURL     string
	CDNURL     string
	Version    string
	HTTPClient *http.Client
}

// Config is saved only by settings after QR confirmation. Owner is the authenticated scanner.
type Config struct {
	Token   string
	BaseURL string
	BotID   string
	Owner   string
}

// Validate refuses incomplete login results and credential-bearing URLs.
func (c Config) Validate() error {
	if c.Token == "" || c.BotID == "" || c.Owner == "" || strings.Contains(c.Owner, "/") {
		return errors.New("WeChat login returned incomplete credentials")
	}
	return checkURL(c.BaseURL)
}

func checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return errors.New("invalid WeChat endpoint")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1") {
		return nil
	}
	return errors.New("invalid WeChat endpoint")
}

type client struct {
	options Options
	http    *http.Client
}

func newClient(options Options) *client {
	if options.APIURL == "" {
		options.APIURL = defaultAPI
	}
	if options.CDNURL == "" {
		options.CDNURL = defaultCDN
	}
	if options.Version == "" {
		options.Version = "dev"
	}
	var hc http.Client
	if options.HTTPClient != nil {
		hc = *options.HTTPClient
	}
	// A business redirect must not forward credentials. QR redirects are explicit protocol states.
	hc.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &client{options: options, http: &hc}
}

func (c *client) call(ctx context.Context, base, token, method, endpoint string, body map[string]any, result any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var reader io.Reader
	if method == http.MethodPost {
		if !strings.HasPrefix(endpoint, "get_bot_qrcode") {
			body["base_info"] = map[string]string{"channel_version": channelVersion, "bot_agent": "dex/" + c.options.Version}
		}
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+"/ilink/bot/"+endpoint, reader)
	if err != nil {
		return errors.New("invalid WeChat request")
	}
	req.Header.Set("iLink-App-Id", "bot")
	req.Header.Set("iLink-App-ClientVersion", "132106") // 2<<16 | 4<<8 | 10
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("AuthorizationType", "ilink_bot_token")
		var random [4]byte
		if _, err = rand.Read(random[:]); err != nil {
			return err
		}
		req.Header.Set("X-WECHAT-UIN", base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(binary.BigEndian.Uint32(random[:])), 10))))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return networkError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return &apiError{HTTPStatus: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	var envelope struct {
		Ret     int    `json:"ret"`
		Code    int    `json:"errcode"`
		Message string `json:"errmsg"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode WeChat response: %w", err)
	}
	if envelope.Ret == -14 || envelope.Code == -14 {
		return ErrSessionExpired
	}
	if envelope.Ret != 0 || envelope.Code != 0 {
		return &apiError{Ret: envelope.Ret, Code: envelope.Code, InvalidUser: strings.Contains(envelope.Message, "invalid user") || strings.Contains(envelope.Message, "invalid to_user_id")}
	}
	return json.Unmarshal(raw, result)
}

type apiError struct {
	HTTPStatus  int
	Ret         int
	Code        int
	InvalidUser bool
}

func (e *apiError) Error() string {
	return fmt.Sprintf("WeChat API error (HTTP %d, ret %d, code %d)", e.HTTPStatus, e.Ret, e.Code)
}

// URL errors contain QR values, context tokens, and CDN credentials. Keep only the cause.
func networkError(err error) error {
	if uerr, ok := errors.AsType[*url.Error](err); ok {
		return fmt.Errorf("WeChat request: %w", uerr.Err)
	}
	return err
}
