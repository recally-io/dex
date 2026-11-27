package main

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"time"

	"github.com/nsgo-dev/nsgo/core"
)

func botPath(id string) string { return "/api/bots/" + url.PathEscape(id) }

var errSignIn = errors.New("请使用 dex 访问令牌登录")

func gatewayRequest(ctx context.Context, method, path, accept string, body io.Reader, contentType string, headers ...http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, apiURL(path), body)
	if err != nil {
		return nil, err
	}
	for _, h := range headers {
		for key, values := range h {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	}
	req.Header.Set("Accept", accept)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if runtime.GOOS == "js" {
		req.Header.Set("js.fetch:redirect", "error")
	}
	res, err := chatClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return res, nil
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return nil, errSignIn
	}
	var problem struct {
		Error string `json:"error"`
	}
	if err := json.UnmarshalRead(io.LimitReader(res.Body, 8192), &problem); err == nil && problem.Error != "" {
		return nil, fmt.Errorf("%s: %s", res.Status, problem.Error)
	}
	return nil, errors.New(res.Status)
}

func requestJSON(ctx context.Context, method, path string, body, out any) error {
	var input io.Reader
	var contentType string
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input, contentType = bytes.NewReader(data), "application/json"
	}
	res, err := gatewayRequest(ctx, method, path, "application/json", input, contentType)
	if err != nil {
		return err
	}
	defer closeChatBody(ctx, res.Body)()
	if out == nil || res.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.UnmarshalRead(res.Body, out)
}

// Leaving the page cancels its request. A disposed scope drops queued UI updates.
func work[T any](cx *core.Scope, run func(context.Context) (T, error), done func(T, error)) {
	ctx, cancel := context.WithTimeout(cx.Context(), 20*time.Second)
	go func() {
		defer cancel()
		v, err := run(ctx)
		cx.Post(func() { done(v, err) })
	}()
}
