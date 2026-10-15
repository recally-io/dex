package feishu

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Registration is a device session. Credentials never leave the settings service.
type Registration struct {
	DeviceCode      string `json:"device_code"`
	VerificationURI string `json:"verification_uri"`
	QRURL           string `json:"verification_uri_complete"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
	AppID           string `json:"client_id"`
	AppSecret       string `json:"client_secret"`
	UserInfo        struct {
		OpenID string `json:"open_id"`
		Brand  string `json:"tenant_brand"`
	} `json:"user_info"`
	Error string `json:"error"`
}

// Registrar runs the app creation flow. AccountsURL replaces both brands in tests.
type Registrar struct{ AccountsURL string }

func (r Registrar) post(ctx context.Context, brand string, form url.Values) (Registration, error) {
	base := r.AccountsURL
	if base == "" {
		base = "https://accounts.feishu.cn"
		if brand == "lark" {
			base = "https://accounts.larksuite.com"
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/oauth/v1/app/registration", strings.NewReader(form.Encode()))
	if err != nil {
		return Registration{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return Registration{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	var data Registration
	if err := json.UnmarshalRead(io.LimitReader(resp.Body, 1<<20), &data); err != nil {
		return data, err
	}
	if data.Error != "" && data.Error != "authorization_pending" && data.Error != "slow_down" {
		return Registration{}, errors.New("feishu registration was refused or expired")
	}
	if resp.StatusCode >= 400 && data.Error == "" {
		return Registration{}, fmt.Errorf("feishu registration HTTP %d", resp.StatusCode)
	}
	return data, nil
}

// Begin requests only private messages and resource access.
func (r Registrar) Begin(ctx context.Context, brand string) (Registration, error) {
	data, err := r.post(ctx, brand, url.Values{"action": {"begin"}, "archetype": {"PersonalAgent"}, "auth_method": {"client_secret"}, "request_user_info": {"open_id tenant_brand"}})
	if err != nil {
		return data, err
	}
	raw := data.QRURL
	if raw == "" {
		raw = data.VerificationURI
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || data.DeviceCode == "" {
		return Registration{}, errors.New("feishu returned an invalid device session")
	}
	addons := map[string]any{"scopes": map[string]any{"tenant": []string{"im:message", "im:message.p2p_msg:readonly", "im:resource"}}, "events": map[string]any{"items": map[string]any{"tenant": []string{"im.message.receive_v1"}}}}
	b, err := json.Marshal(addons)
	if err != nil {
		return Registration{}, err
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(b); err != nil {
		return Registration{}, err
	}
	if err := writer.Close(); err != nil {
		return Registration{}, err
	}
	q := u.Query()
	q.Set("from", "sdk")
	q.Set("tp", "sdk")
	q.Set("source", "go-sdk/dex")
	q.Set("createOnly", "true")
	q.Set("addons", base64.RawURLEncoding.EncodeToString(compressed.Bytes()))
	u.RawQuery = q.Encode()
	data.QRURL = u.String()
	if data.Interval <= 0 {
		data.Interval = 5
	}
	if data.ExpiresIn <= 0 {
		data.ExpiresIn = 300
	}
	return data, nil
}

// Poll follows a Lark tenant to the Lark accounts service.
func (r Registrar) Poll(ctx context.Context, brand, deviceCode string) (Registration, error) {
	data, err := r.post(ctx, brand, url.Values{"action": {"poll"}, "device_code": {deviceCode}})
	if err != nil {
		return data, err
	}
	if data.Error != "" {
		if data.Interval <= 0 {
			data.Interval = 5
			if data.Error == "slow_down" {
				data.Interval = 10
			}
		}
		return data, nil
	}
	if data.AppSecret == "" && data.UserInfo.Brand == "lark" && brand != "lark" {
		return r.Poll(ctx, "lark", deviceCode)
	}
	if data.AppID == "" || data.AppSecret == "" || data.UserInfo.OpenID == "" {
		return Registration{}, errors.New("feishu registration did not return credentials and owner")
	}
	return data, nil
}
