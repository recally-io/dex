// Package speech implements an explicitly configured OpenAI-compatible audio boundary.
package speech

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Options struct {
	Enabled            bool   `json:"enabled"`
	AllowSending       bool   `json:"allowSending"`
	Provider           string `json:"provider,omitempty"`
	Model              string `json:"model,omitempty"`
	Voice              string `json:"voice,omitempty"`
	Language           string `json:"language,omitempty"`
	Format             string `json:"format,omitempty"`
	TimeoutSeconds     int    `json:"timeoutSeconds,omitzero"`
	MaxBytes           int64  `json:"maxBytes,omitzero"`
	MaxDurationSeconds int    `json:"maxDurationSeconds,omitzero"`
	MaxTextCharacters  int    `json:"maxTextCharacters,omitzero"`
}

func (s Options) Budgets() Options {
	if s.TimeoutSeconds == 0 {
		s.TimeoutSeconds = 30
	}
	if s.MaxBytes == 0 {
		s.MaxBytes = 20 << 20
	}
	if s.MaxDurationSeconds == 0 {
		s.MaxDurationSeconds = 300
	}
	if s.MaxTextCharacters == 0 {
		s.MaxTextCharacters = 4000
	}
	if s.Format == "" {
		s.Format = "opus"
	}
	return s
}

// Endpoint is resolved at the call boundary. The key is never part of saved channel options.
type Endpoint struct {
	URL    string
	APIKey string
}
type Resolve func(context.Context, string) (Endpoint, error)

// Call returns a transcript or generated audio. It does not retry or choose another provider.
func Call(ctx context.Context, endpoint Endpoint, options Options, kind, name, text string, audio io.Reader) (string, []byte, error) {
	if !options.Enabled || !options.AllowSending {
		return "", nil, errors.New("speech is off or sending consent is missing")
	}
	options = options.Budgets()
	base, err := url.Parse(endpoint.URL)
	if err != nil {
		return "", nil, errors.New("speech provider URL is invalid")
	}
	loopback := base.Hostname() == "localhost" || base.Hostname() == "127.0.0.1" || base.Hostname() == "::1"
	validScheme := base.Scheme == "https" || base.Scheme == "http" && loopback
	if base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || !validScheme {
		return "", nil, errors.New("speech requires an explicit HTTPS provider endpoint or loopback HTTP")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(options.TimeoutSeconds)*time.Second)
	defer cancel()
	var body bytes.Buffer
	contentType := "application/json"
	route := "/audio/speech"
	if kind == "stt" {
		route = "/audio/transcriptions"
		form := multipart.NewWriter(&body)
		if err := form.WriteField("model", options.Model); err != nil {
			return "", nil, err
		}
		if options.Language != "" {
			if err := form.WriteField("language", options.Language); err != nil {
				return "", nil, err
			}
		}
		part, err := form.CreateFormFile("file", name)
		if err != nil {
			return "", nil, err
		}
		n, err := io.Copy(part, io.LimitReader(audio, options.MaxBytes+1))
		if err != nil {
			return "", nil, err
		}
		if n > options.MaxBytes {
			return "", nil, errors.New("speech input exceeds configured byte budget")
		}
		if err := form.Close(); err != nil {
			return "", nil, err
		}
		contentType = form.FormDataContentType()
	} else {
		encoded, err := json.Marshal(map[string]any{"model": options.Model, "input": text, "voice": options.Voice, "response_format": options.Format})
		if err != nil {
			return "", nil, err
		}
		body.Write(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint.URL, "/")+route, &body)
	if err != nil {
		return "", nil, errors.New("speech request is invalid")
	}
	req.Header.Set("Content-Type", contentType)
	if endpoint.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+endpoint.APIKey)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return "", nil, errors.New("speech provider outcome unknown; automatic retry is disabled")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode >= http.StatusInternalServerError {
			return "", nil, fmt.Errorf("speech provider HTTP %d; remote outcome unknown; automatic retry is disabled", response.StatusCode)
		}
		return "", nil, fmt.Errorf("speech provider rejected the request: HTTP %d; automatic retry is disabled", response.StatusCode)
	}
	limit := options.MaxBytes
	if kind == "stt" {
		limit = min(limit, int64(65536))
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return "", nil, errors.New("speech provider response incomplete; outcome unknown; automatic retry is disabled")
	}
	if int64(len(payload)) > limit {
		return "", nil, errors.New("speech response exceeds configured byte budget")
	}
	if kind == "stt" {
		var result struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(payload, &result); err != nil {
			return "", nil, errors.New("speech provider returned an invalid transcript")
		}
		result.Text = strings.TrimSpace(result.Text)
		if result.Text == "" {
			return "", nil, errors.New("speech provider returned an empty transcript")
		}
		return result.Text, nil, nil
	}
	if len(payload) == 0 {
		return "", nil, errors.New("speech provider returned empty audio")
	}
	mime := http.DetectContentType(payload)
	if !strings.HasPrefix(mime, "audio/") && mime != "application/ogg" && mime != "application/octet-stream" {
		return "", nil, errors.New("speech provider did not return audio bytes")
	}
	return "", payload, nil
}
