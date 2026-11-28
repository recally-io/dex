package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	localWorkspaceTarget = "local"
	localAppleOrigin     = "dex-local://workspace"
	localHost            = "local.dex.internal"
	localAndroidOrigin   = "https://" + localHost
)

//go:embed localbridge.js
var localBridgeJS string

var localScriptHash = sync.OnceValue(func() string {
	hash := sha256.Sum256([]byte(localBridgeJS))
	return "'sha256-" + base64.StdEncoding.EncodeToString(hash[:]) + "'"
})

type localBridge struct {
	handler http.Handler
	mu      sync.Mutex
	streams map[string]*localStream
}

type localStream struct{ cancel context.CancelFunc }

type localRequest struct {
	Method string      `json:"method"`
	URL    string      `json:"url"`
	Header http.Header `json:"headers"`
	Body   []byte      `json:"body"`
}

type localResponse struct {
	Status  int         `json:"status,omitzero"`
	Headers http.Header `json:"headers,omitempty"`
	Body    []byte      `json:"body,omitempty"`
	Error   string      `json:"error,omitempty"`
}

func (b *localBridge) ServeStatic(ctx context.Context, method, rawURL string, headers http.Header) (localResponse, error) {
	if method != http.MethodGet && method != http.MethodHead {
		return localResponse{Status: http.StatusMethodNotAllowed}, nil
	}
	req, err := localHTTP(ctx, localRequest{Method: method, URL: rawURL, Header: headers}, false)
	if err != nil {
		return localResponse{}, err
	}
	response := b.serve(req)
	if strings.HasPrefix(response.Headers.Get("Content-Type"), "text/html") {
		// Android exposes its Java interface to all frames. Allow only the
		// workspace scripts, and prevent other documents from using that interface.
		policy := "frame-src 'none'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; script-src 'self'"
		if strings.HasPrefix(rawURL, localAndroidOrigin) && method == http.MethodGet {
			if before, after, found := bytes.Cut(response.Body, []byte("<head>")); found {
				response.Body = bytes.Join([][]byte{before, []byte("<head><script>"), []byte(localBridgeJS), []byte("</script>"), after}, nil)
				response.Headers.Del("Content-Length")
				policy += " " + localScriptHash()
			}
		}
		response.Headers.Add("Content-Security-Policy", policy)
	}
	return response, nil
}

func (b *localBridge) Fetch(ctx context.Context, input localRequest) (localResponse, error) {
	switch input.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return localResponse{Status: http.StatusMethodNotAllowed}, nil
	}
	req, err := localHTTP(ctx, input, true)
	if err != nil {
		return localResponse{}, err
	}
	return b.serve(req), nil
}

func localHTTP(ctx context.Context, input localRequest, apiOnly bool) (*http.Request, error) {
	u, err := url.Parse(input.URL)
	if err != nil || u.User != nil || u.Opaque != "" || u.Fragment != "" ||
		!(u.Scheme == "" && u.Host == "" || u.Scheme+"://"+u.Host == localAppleOrigin || u.Scheme+"://"+u.Host == localAndroidOrigin) ||
		strings.HasPrefix(input.URL, "//") {
		return nil, errors.New("invalid local workspace URL")
	}
	if u.Path == "" && u.Host != "" {
		u.Path = "/"
	}
	if !strings.HasPrefix(u.Path, "/") || strings.Contains(u.Path, "//") || (apiOnly && !strings.HasPrefix(u.EscapedPath(), "/api/")) {
		return nil, errors.New("invalid local workspace path")
	}
	for segment := range strings.SplitSeq(u.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, errors.New("invalid local workspace path")
		}
	}
	// The native view checks the caller and the URL before reaching this point.
	// This is not an HTTP peer. Do not carry browser cross-origin metadata to
	// the synthetic request or change the server's cross-origin middleware.
	req, err := http.NewRequestWithContext(ctx, input.Method, u.RequestURI(), bytes.NewReader(input.Body))
	if err != nil {
		return nil, err
	}
	req.Host = localHost
	req.RequestURI = u.RequestURI()
	for key, values := range input.Header {
		switch http.CanonicalHeaderKey(key) {
		case "Origin", "Sec-Fetch-Site", "Host", "Content-Length":
			continue
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	return req, nil
}

func (b *localBridge) serve(req *http.Request) localResponse {
	w := localWriter{header: make(http.Header)}
	b.handler.ServeHTTP(&w, req)
	w.WriteHeader(http.StatusOK)
	var body []byte
	if req.Method != http.MethodHead {
		body = w.body.Bytes()
	}
	return localResponse{Status: w.status, Headers: w.sent, Body: body}
}

// OpenSSE starts a stream. It delivers each flush before the handler returns.
// onEnd runs when the handler has returned, including after CloseSSE.
func (b *localBridge) OpenSSE(ctx context.Context, id, rawURL, lastEventID string, onChunk func([]byte), onEnd func(error)) error {
	ctx, cancel := context.WithCancel(ctx)
	req, err := localHTTP(ctx, localRequest{Method: http.MethodGet, URL: rawURL, Header: http.Header{
		"Accept": {"text/event-stream"}, "Last-Event-ID": {lastEventID},
	}}, true)
	if err != nil {
		cancel()
		return err
	}
	stream := &localStream{cancel: cancel}
	b.mu.Lock()
	if b.streams[id] != nil {
		b.mu.Unlock()
		cancel()
		return errors.New("local event stream already open")
	}
	if b.streams == nil {
		b.streams = make(map[string]*localStream)
	}
	b.streams[id] = stream
	b.mu.Unlock()
	go func() {
		defer cancel()
		w := &localStreamWriter{localWriter: localWriter{header: make(http.Header)}, ctx: ctx, send: onChunk}
		b.handler.ServeHTTP(w, req)
		if ctx.Err() == nil {
			w.Flush()
		}
		b.mu.Lock()
		delete(b.streams, id)
		b.mu.Unlock()
		onEnd(w.err)
	}()
	return nil
}

func (b *localBridge) CloseSSE(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if stream := b.streams[id]; stream != nil {
		stream.cancel()
	}
}

type localWriter struct {
	header, sent http.Header
	status       int
	body         bytes.Buffer
}

func (w *localWriter) Header() http.Header { return w.header }

func (w *localWriter) WriteHeader(status int) {
	if w.status == 0 && status >= 200 {
		w.status, w.sent = status, w.header.Clone()
	}
}

func (w *localWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		if _, ok := w.header["Content-Type"]; !ok && len(p) > 0 {
			w.header.Set("Content-Type", http.DetectContentType(p))
		}
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(p)
}

type localStreamWriter struct {
	localWriter
	ctx    context.Context
	send   func([]byte)
	opened bool
	err    error
}

func (w *localStreamWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.localWriter.Write(p)
}

func (w *localStreamWriter) Flush() { _ = w.FlushError() }

func (w *localStreamWriter) FlushError() error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	if !w.opened {
		w.WriteHeader(http.StatusOK)
		kind, _, _ := mime.ParseMediaType(w.sent.Get("Content-Type"))
		if w.status != http.StatusOK || kind != "text/event-stream" {
			w.err = fmt.Errorf("local event stream: status %d, content type %q", w.status, kind)
			return w.err
		}
	} else if w.body.Len() == 0 {
		return nil
	}
	w.opened = true
	w.send(bytes.Clone(w.body.Bytes()))
	w.body.Reset()
	return nil
}

// localMessage is the wire format of the local WebView, not an HTTP protocol.
type localMessage struct {
	Op          string `json:"op"`
	ID          string `json:"id"`
	LastEventID string `json:"lastEventId"`
	localRequest
}

func (b *localBridge) handleMessage(ctx context.Context, raw string, static bool, send func(string, any)) {
	var msg localMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil || msg.ID == "" || static != (msg.Op == "static") {
		return
	}
	switch msg.Op {
	case "fetch":
		go func() {
			response, err := b.Fetch(ctx, msg.localRequest)
			if err != nil {
				response.Error = err.Error()
			}
			send("eval", localJSEvent("fetch", msg.ID, response))
		}()
	case "static":
		go func() {
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			response, err := b.ServeStatic(ctx, msg.Method, msg.URL, msg.Header)
			if err != nil {
				response.Status = http.StatusBadRequest
			}
			result, err := json.Marshal(localStaticResult{ID: msg.ID, Response: response})
			if err == nil {
				send("staticResult", string(result))
			}
		}()
	case "sseOpen":
		end := func(err error) {
			send("eval", localJSEvent("end", msg.ID, err != nil))
		}
		if err := b.OpenSSE(ctx, msg.ID, msg.URL, msg.LastEventID, func(chunk []byte) {
			send("eval", localJSEvent("chunk", msg.ID, chunk))
		}, end); err != nil {
			end(err)
		}
	case "sseClose":
		b.CloseSSE(msg.ID)
	}
}

type localStaticResult struct {
	ID       string        `json:"id"`
	Response localResponse `json:"response"`
}

func localJSEvent(kind, id string, data any) string {
	payload, err := json.Marshal([]any{kind, id, data})
	if err != nil {
		return ""
	}
	return "window.__dexLocalReceive?.(..." + string(payload) + ")"
}
