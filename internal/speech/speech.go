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
	Language           string `json:"language,omitempty"`
	TimeoutSeconds     int    `json:"timeoutSeconds,omitzero"`
	MaxBytes           int64  `json:"maxBytes,omitzero"`
	MaxDurationSeconds int    `json:"maxDurationSeconds,omitzero"`
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
	return s
}

// Endpoint is resolved at the call boundary. The key is never part of saved channel options.
type Endpoint struct {
	URL    string
	APIKey string
}
type Resolve func(context.Context, string) (Endpoint, error)

// ErrOutcomeUnknown marks a call that may have reached the provider: a transport failure,
// a 5xx response or an incomplete response body. Callers must not send it again automatically.
var ErrOutcomeUnknown = errors.New("speech provider outcome unknown")

// unknownError keeps the public message while matching ErrOutcomeUnknown.
type unknownError string

func (e unknownError) Error() string        { return string(e) }
func (e unknownError) Is(target error) bool { return target == ErrOutcomeUnknown }

// Transcribe returns a transcript. It does not retry or choose another provider.
// Errors that match ErrOutcomeUnknown leave the remote result unknown; other errors are definite failures.
func Transcribe(ctx context.Context, endpoint Endpoint, options Options, name string, audio io.Reader) (string, error) {
	if !options.Enabled || !options.AllowSending {
		return "", errors.New("speech is off or sending consent is missing")
	}
	options = options.Budgets()
	base, err := url.Parse(endpoint.URL)
	if err != nil {
		return "", errors.New("speech provider URL is invalid")
	}
	loopback := base.Hostname() == "localhost" || base.Hostname() == "127.0.0.1" || base.Hostname() == "::1"
	validScheme := base.Scheme == "https" || base.Scheme == "http" && loopback
	if base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || !validScheme {
		return "", errors.New("speech requires an explicit HTTPS provider endpoint or loopback HTTP")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(options.TimeoutSeconds)*time.Second)
	defer cancel()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("model", options.Model); err != nil {
		return "", err
	}
	if options.Language != "" {
		if err := form.WriteField("language", options.Language); err != nil {
			return "", err
		}
	}
	part, err := form.CreateFormFile("file", name)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(part, io.LimitReader(audio, options.MaxBytes+1))
	if err != nil {
		return "", err
	}
	if n > options.MaxBytes {
		return "", errors.New("speech input exceeds configured byte budget")
	}
	if err := form.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint.URL, "/")+"/audio/transcriptions", &body)
	if err != nil {
		return "", errors.New("speech request is invalid")
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	if endpoint.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+endpoint.APIKey)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return "", unknownError("speech provider outcome unknown; automatic retry is disabled")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode >= http.StatusInternalServerError {
			return "", unknownError(fmt.Sprintf("speech provider HTTP %d; remote outcome unknown; automatic retry is disabled", response.StatusCode))
		}
		return "", fmt.Errorf("speech provider rejected the request: HTTP %d; automatic retry is disabled", response.StatusCode)
	}
	limit := min(options.MaxBytes, int64(65536))
	payload, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return "", unknownError("speech provider response incomplete; outcome unknown; automatic retry is disabled")
	}
	if int64(len(payload)) > limit {
		return "", errors.New("speech response exceeds configured byte budget")
	}
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return "", errors.New("speech provider returned an invalid transcript")
	}
	result.Text = strings.TrimSpace(result.Text)
	if result.Text == "" {
		return "", errors.New("speech provider returned an empty transcript")
	}
	return result.Text, nil
}
