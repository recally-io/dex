package execenv

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	e2bEnvdPort          = 49983
	maxConnectFrameBytes = 16 * 1024 * 1024
)

type protoInt64 int64

func (v *protoInt64) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() == '"' {
		n, err := strconv.ParseInt(tok.String(), 10, 64)
		*v = protoInt64(n)
		return err
	}
	if tok.Kind() == '0' {
		n, err := tok.Int()
		*v = protoInt64(n)
		return err
	}
	return fmt.Errorf("unexpected proto int64 token %v", tok.Kind())
}

type envdConnectError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *envdConnectError) Error() string { return e.Code + ": " + e.Message }

type envdEntry struct {
	Name          string     `json:"name"`
	Type          string     `json:"type"`
	Path          string     `json:"path"`
	Size          protoInt64 `json:"size"`
	ModifiedTime  *time.Time `json:"modifiedTime,omitempty"`
	SymlinkTarget *string    `json:"symlinkTarget,omitempty"`
}

type envdProcessEvent struct {
	Start *struct {
		PID uint32 `json:"pid"`
	} `json:"start,omitempty"`
	Data *struct {
		Stdout []byte `json:"stdout,omitempty"`
		Stderr []byte `json:"stderr,omitempty"`
	} `json:"data,omitempty"`
	End *struct {
		ExitCode int     `json:"exitCode"`
		Exited   bool    `json:"exited"`
		Error    *string `json:"error,omitempty"`
	} `json:"end,omitempty"`
	Keepalive *struct{} `json:"keepalive,omitempty"`
}

type envdClient struct {
	baseURL     string
	sandboxID   string
	accessToken string
	httpClient  *http.Client
}

func (c *envdClient) applyHeaders(req *http.Request) {
	if c.accessToken != "" {
		req.Header.Set("X-Access-Token", c.accessToken)
	}
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("user:")))
	if c.sandboxID != "" {
		req.Header.Set("E2b-Sandbox-Id", c.sandboxID)
		req.Header.Set("E2b-Sandbox-Port", strconv.Itoa(e2bEnvdPort))
	}
}

func (c *envdClient) filesURL(filePath string) string {
	q := url.Values{}
	q.Set("path", filePath)
	q.Set("username", "user")
	return c.baseURL + "/files?" + q.Encode()
}

func (c *envdClient) getEnvs(ctx context.Context) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/envs", nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseEnvdHTTPError(resp)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c *envdClient) downloadFile(ctx context.Context, filePath string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.filesURL(filePath), nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseEnvdHTTPError(resp)
	}
	return io.ReadAll(resp.Body)
}

func (c *envdClient) downloadRange(ctx context.Context, filePath string, offset, length int64) ([]byte, error) {
	if length <= 0 {
		return []byte{}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.filesURL(filePath), nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return []byte{}, nil
	}
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return nil, parseEnvdHTTPError(resp)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		if offset >= int64(len(data)) {
			return []byte{}, nil
		}
		data = data[offset:min(int64(len(data)), offset+length)]
	}
	return data, nil
}

func (c *envdClient) uploadFile(ctx context.Context, filePath string, data []byte) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filePath)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.filesURL(filePath), &body)
	if err != nil {
		return err
	}
	c.applyHeaders(req)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseEnvdHTTPError(resp)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *envdClient) stat(ctx context.Context, filePath string) (*envdEntry, error) {
	var resp struct {
		Entry *envdEntry `json:"entry"`
	}
	if err := c.callUnary(ctx, "/filesystem.Filesystem/Stat", map[string]string{"path": filePath}, &resp); err != nil {
		return nil, err
	}
	if resp.Entry == nil {
		return nil, &envdConnectError{Code: "not_found", Message: "missing entry"}
	}
	return resp.Entry, nil
}

func (c *envdClient) makeDir(ctx context.Context, dirPath string) error {
	return c.callUnary(ctx, "/filesystem.Filesystem/MakeDir", map[string]string{"path": dirPath}, nil)
}

func (c *envdClient) listDir(ctx context.Context, dirPath string) ([]envdEntry, error) {
	var resp struct {
		Entries []envdEntry `json:"entries"`
	}
	err := c.callUnary(ctx, "/filesystem.Filesystem/ListDir", map[string]any{"path": dirPath, "depth": 1}, &resp)
	return resp.Entries, err
}

func (c *envdClient) remove(ctx context.Context, targetPath string) error {
	return c.callUnary(ctx, "/filesystem.Filesystem/Remove", map[string]string{"path": targetPath}, nil)
}

func (c *envdClient) sendSignal(ctx context.Context, pid uint32, sig string) error {
	return c.callUnary(ctx, "/process.Process/SendSignal", map[string]any{
		"process": map[string]uint32{"pid": pid},
		"signal":  sig,
	}, nil)
}

func (c *envdClient) startProcess(ctx context.Context, procCfg map[string]any, onEvent func(envdProcessEvent) error) error {
	encoded, err := json.Marshal(map[string]any{"process": procCfg, "stdin": false})
	if err != nil {
		return err
	}
	frame := make([]byte, 5+len(encoded))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(encoded))) //nolint:gosec // bounded RPC payload.
	copy(frame[5:], encoded)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/process.Process/Start", bytes.NewReader(frame))
	if err != nil {
		return err
	}
	c.applyHeaders(req)
	req.Header.Set("Content-Type", "application/connect+json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Keepalive-Ping-Interval", "50")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/connect+") {
		return parseEnvdHTTPError(resp)
	}

	for {
		var hdr [5]byte
		n, err := io.ReadFull(resp.Body, hdr[:])
		if n == 0 && errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		flags := hdr[0]
		length := binary.BigEndian.Uint32(hdr[1:5])
		if length > maxConnectFrameBytes {
			return fmt.Errorf("envd frame %d exceeds limit", length)
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(resp.Body, payload); err != nil {
			return err
		}
		if flags&0x01 != 0 {
			zr, gzErr := gzip.NewReader(bytes.NewReader(payload))
			if gzErr != nil {
				return gzErr
			}
			payload, err = io.ReadAll(io.LimitReader(zr, maxConnectFrameBytes))
			_ = zr.Close()
			if err != nil {
				return err
			}
		}
		if flags&0x02 != 0 {
			var endFrame struct {
				Error *envdConnectError `json:"error,omitempty"`
			}
			if len(bytes.TrimSpace(payload)) > 0 {
				_ = json.Unmarshal(payload, &endFrame)
			}
			if endFrame.Error != nil {
				return endFrame.Error
			}
			return nil
		}
		var msg struct {
			Event *envdProcessEvent `json:"event,omitempty"`
		}
		if err := json.Unmarshal(payload, &msg); err != nil {
			return err
		}
		if msg.Event != nil && msg.Event.Keepalive == nil {
			if err := onEvent(*msg.Event); err != nil {
				return err
			}
		}
	}
}

func (c *envdClient) callUnary(ctx context.Context, procedure string, reqBody any, respOut any) error {
	encoded, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+procedure, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	c.applyHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return parseEnvdHTTPError(resp)
	}
	if respOut == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return err
	}
	return json.Unmarshal(raw, respOut)
}

func parseEnvdHTTPError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var ce envdConnectError
	if json.Unmarshal(raw, &ce) == nil && ce.Code != "" {
		return &ce
	}
	code := "unknown"
	switch resp.StatusCode {
	case http.StatusNotFound:
		code = "not_found"
	case http.StatusForbidden, http.StatusUnauthorized:
		code = "permission_denied"
	case http.StatusBadRequest:
		code = "invalid_argument"
	case http.StatusConflict:
		code = "already_exists"
	}
	return &envdConnectError{Code: code, Message: cmp.Or(strings.TrimSpace(string(raw)), resp.Status)}
}

func makeEnvdDirAll(ctx context.Context, envd *envdClient, dirPath string) error {
	cleaned := path.Clean(dirPath)
	if cleaned == "/" || cleaned == "." {
		return nil
	}
	var cur strings.Builder
	for seg := range strings.SplitSeq(strings.TrimPrefix(cleaned, "/"), "/") {
		if seg == "" {
			continue
		}
		cur.WriteByte('/')
		cur.WriteString(seg)
		dir := cur.String()
		if err := envd.makeDir(ctx, dir); err != nil {
			if ce, ok := errors.AsType[*envdConnectError](err); ok && ce.Code == "already_exists" {
				continue
			}
			if entry, statErr := envd.stat(ctx, dir); statErr == nil && entry.Type == "FILE_TYPE_DIRECTORY" {
				continue
			}
			return err
		}
	}
	return nil
}

func probeEnvdPlatformArch(ctx context.Context, envd *envdClient) (platform, arch string) {
	platform, arch = "linux", "x64"
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	err := envd.startProcess(probeCtx, map[string]any{
		"cmd":  "/bin/sh",
		"args": []string{"-lc", "uname -sm"},
	}, func(ev envdProcessEvent) error {
		if ev.Data != nil && len(ev.Data.Stdout) > 0 {
			stdout.Write(ev.Data.Stdout)
		}
		return nil
	})
	if err != nil {
		return platform, arch
	}
	fields := strings.Fields(strings.TrimSpace(stdout.String()))
	if len(fields) >= 1 {
		switch strings.ToLower(fields[0]) {
		case "linux", "darwin":
			platform = strings.ToLower(fields[0])
		}
	}
	if len(fields) >= 2 {
		switch strings.ToLower(fields[1]) {
		case "x86_64", "amd64":
			arch = "x64"
		case "aarch64", "arm64":
			arch = "arm64"
		}
	}
	return platform, arch
}
