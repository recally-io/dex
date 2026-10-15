package mcptest_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/mcptest"
)

type obj = map[string]any

type reply struct {
	Result struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools struct {
				ListChanged bool `json:"listChanged"`
			} `json:"tools"`
		} `json:"capabilities"`
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Tools []struct {
			Name        string `json:"name"`
			InputSchema obj    `json:"inputSchema"`
		} `json:"tools"`
		NextCursor string `json:"nextCursor"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error struct {
		Code int `json:"code"`
	} `json:"error"`
}

func decode(t *testing.T, data []byte) reply {
	t.Helper()
	var result reply
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return result
}

func message(method string, params any) []byte {
	data, err := json.Marshal(obj{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		panic(err)
	}
	return data
}

func checkText(t *testing.T, r reply, want string, isError bool) {
	t.Helper()
	if r.Error.Code != 0 || len(r.Result.Content) != 1 || r.Result.Content[0].Type != "text" || r.Result.Content[0].Text != want || r.Result.IsError != isError {
		t.Fatalf("reply = %+v, want text %q, isError %v", r, want, isError)
	}
}

func poll(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-deadline.C:
			t.Fatal("condition not observed within 3s")
		case <-ticker.C:
		}
	}
}

func exercise(t *testing.T, dir string, call func(string, any) reply) {
	t.Helper()
	// Given an initialized client, all pages expose usable tool schemas.
	var names []string
	cursor := ""
	for page := range 3 {
		r := call("tools/list", obj{"cursor": cursor})
		if len(r.Result.Tools) != min(2, 5-page*2) {
			t.Fatalf("page %d: %+v", page, r)
		}
		for _, tool := range r.Result.Tools {
			if tool.InputSchema["type"] != "object" || tool.InputSchema["properties"] == nil {
				t.Fatalf("schema: %+v", tool)
			}
			names = append(names, tool.Name)
		}
		cursor = r.Result.NextCursor
		if (page == 2) != (cursor == "") {
			t.Fatalf("page %d cursor %q", page, cursor)
		}
	}
	if !reflect.DeepEqual(names, []string{"append", "read", "fail", "slow_echo", "wait"}) {
		t.Fatal(names)
	}
	tool := func(name string, args obj) reply { return call("tools/call", obj{"name": name, "arguments": args}) }
	// When tools run, the caller observes real file changes and tool failures.
	checkText(t, tool("read", obj{}), "", false)
	checkText(t, tool("append", obj{"text": "first"}), "appended 1", false)
	checkText(t, tool("append", obj{"text": "second"}), "appended 2", false)
	checkText(t, tool("read", obj{}), "first\nsecond\n", false)
	checkText(t, tool("fail", obj{}), "failed on purpose", true)
	checkText(t, tool("slow_echo", obj{"text": "hello"}), "echo: hello", false)
	released := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		for {
			data, err := os.ReadFile(filepath.Join(dir, "notes.txt"))
			if err == nil && strings.Contains(string(data), "started ready\n") {
				released <- os.WriteFile(filepath.Join(dir, "ready.release"), nil, 0600)
				return
			}
			select {
			case <-deadline.C:
				released <- fmt.Errorf("wait did not write its start marker")
				return
			case <-ticker.C:
			}
		}
	}()
	checkText(t, tool("wait", obj{"name": "ready"}), "released ready", false)
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	// Then protocol failures remain distinct from tool failures.
	for _, tc := range []struct {
		method string
		params obj
		code   int
	}{
		{"tools/call", obj{"name": "unknown"}, -32602},
		{"tools/call", obj{"name": "append", "arguments": obj{"text": 7}}, -32602},
		{"tools/list", obj{"cursor": "bogus"}, -32602},
		{"unknown", obj{}, -32601},
	} {
		if r := call(tc.method, tc.params); r.Error.Code != tc.code {
			t.Fatalf("%s: %+v", tc.method, r)
		}
	}
	if r := call("ping", obj{}); r.Error.Code != 0 {
		t.Fatal(r)
	}
	data, err := os.ReadFile(filepath.Join(dir, "ops.log"))
	if err != nil || !strings.Contains(string(data), "tools/list\n") || !strings.Contains(string(data), "tools/call\n") {
		t.Fatalf("ops log %q: %v", data, err)
	}
}

func TestUserStdioTools(t *testing.T) {
	dir := t.TempDir()
	s := &mcptest.Server{Dir: dir}
	input, writer := io.Pipe()
	reader, output := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- s.ServeStdio(input, output); _ = output.Close() }()
	t.Cleanup(func() {
		_ = writer.Close()
		_ = reader.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	scanner := bufio.NewScanner(reader)
	send := func(data []byte) {
		t.Helper()
		if _, err := fmt.Fprintf(writer, "%s\n", data); err != nil {
			t.Fatal(err)
		}
	}
	receive := func() reply {
		t.Helper()
		if !scanner.Scan() {
			t.Fatalf("missing response: %v", scanner.Err())
		}
		return decode(t, scanner.Bytes())
	}
	call := func(method string, params any) reply { send(message(method, params)); return receive() }
	for _, version := range []string{"2025-11-25", "2025-06-18", "2025-03-26", "future"} {
		r := call("initialize", obj{"protocolVersion": version})
		want := version
		if version == "future" {
			want = "2025-06-18"
		}
		if r.Result.ProtocolVersion != want || r.Result.ServerInfo.Name != "mcptest" || r.Result.ServerInfo.Version != "1" {
			t.Fatalf("initialize: %+v", r)
		}
	}
	send([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	exercise(t, dir, call)
	// A blocked call still permits a cancellation notification on stdin.
	send(message("tools/call", obj{"name": "wait", "arguments": obj{"name": "cancel"}}))
	poll(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(dir, "notes.txt"))
		return strings.Contains(string(data), "started cancel\n")
	})
	send([]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`))
	checkText(t, receive(), "context canceled", true)
	// EOF cancels a blocked call before ServeStdio returns.
	send(message("tools/call", obj{"name": "wait", "arguments": obj{"name": "eof"}}))
	poll(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(dir, "notes.txt"))
		return strings.Contains(string(data), "started eof\n")
	})
	_ = writer.Close()
	checkText(t, receive(), "context canceled", true)
}

type httpClient struct {
	t       *testing.T
	server  *httptest.Server
	session string
}

func (c *httpClient) request(ctx context.Context, method string, data []byte, session, version, accept string) (int, http.Header, []byte) {
	c.t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, c.server.URL, bytes.NewReader(data))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Mcp-Session-Id", session)
	req.Header.Set("MCP-Protocol-Version", version)
	req.Header.Set("Accept", accept)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.server.Client().Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			c.t.Error(err)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, body
}

func TestUserHTTPToolsAndSessions(t *testing.T) {
	dir := t.TempDir()
	server := httptest.NewServer(&mcptest.Server{Dir: dir})
	defer server.Close()
	server.Client().Timeout = 5 * time.Second
	c := httpClient{t: t, server: server}
	call := func(method string, params any) reply {
		status, headers, body := c.request(t.Context(), http.MethodPost, message(method, params), c.session, "2025-06-18", "application/json")
		if status != 200 || headers.Get("Content-Type") != "application/json" {
			t.Fatalf("HTTP %d %v %s", status, headers, body)
		}
		if method == "initialize" {
			c.session = headers.Get("Mcp-Session-Id")
		}
		return decode(t, body)
	}
	if r := call("initialize", obj{"protocolVersion": "2025-11-25"}); r.Result.ProtocolVersion != "2025-11-25" || c.session == "" {
		t.Fatalf("initialize: %+v", r)
	}
	status, _, body := c.request(t.Context(), http.MethodPost, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), c.session, "", "")
	if status != 202 || len(body) != 0 {
		t.Fatalf("notification: %d %s", status, body)
	}
	exercise(t, dir, call)
	status, headers, body := c.request(t.Context(), http.MethodPost, message("tools/call", obj{"name": "slow_echo", "arguments": obj{"text": "SSE"}}), c.session, "", "application/json, text/event-stream")
	if status != 200 || headers.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("SSE: %d %v", status, headers)
	}
	data, ok := strings.CutPrefix(string(body), "event: message\ndata: ")
	if !ok || !strings.HasSuffix(data, "\n\n") {
		t.Fatalf("SSE frame: %q", body)
	}
	checkText(t, decode(t, []byte(strings.TrimSuffix(data, "\n\n"))), "echo: SSE", false)
	for _, tc := range []struct {
		method, session, version string
		status                   int
	}{
		{http.MethodPost, "", "", 400}, {http.MethodPost, "unknown", "", 404},
		{http.MethodPost, c.session, "future", 400}, {http.MethodGet, c.session, "", 405},
	} {
		status, _, _ := c.request(t.Context(), tc.method, message("ping", obj{}), tc.session, tc.version, "")
		if status != tc.status {
			t.Fatalf("%+v: status %d", tc, status)
		}
	}
	// Deleting the session releases all blocked requests in that session.
	waitDone := make(chan reply, 1)
	go func() { waitDone <- call("tools/call", obj{"name": "wait", "arguments": obj{"name": "deleted"}}) }()
	poll(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(dir, "notes.txt"))
		return strings.Contains(string(data), "started deleted\n")
	})
	status, _, _ = c.request(t.Context(), http.MethodDelete, nil, c.session, "", "")
	if status != 204 {
		t.Fatalf("delete: %d", status)
	}
	checkText(t, <-waitDone, "context canceled", true)
	status, _, _ = c.request(t.Context(), http.MethodPost, message("ping", obj{}), c.session, "", "")
	if status != 404 {
		t.Fatalf("deleted session: %d", status)
	}
}

func TestUserHTTPDisconnectCancelsWait(t *testing.T) {
	// Given a real connection with a blocked wait call, closing it ends the handler.
	dir := t.TempDir()
	fixture := &mcptest.Server{Dir: dir}
	finished := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { finished <- struct{}{} }()
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	server.Client().Timeout = 5 * time.Second
	c := httpClient{t: t, server: server}
	status, headers, _ := c.request(t.Context(), http.MethodPost, message("initialize", obj{"protocolVersion": "2025-06-18"}), "", "", "")
	if status != 200 {
		t.Fatalf("initialize: %d", status)
	}
	<-finished
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, bytes.NewReader(message("tools/call", obj{"name": "wait", "arguments": obj{"name": "disconnect"}})))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Mcp-Session-Id", headers.Get("Mcp-Session-Id"))
	clientDone := make(chan error, 1)
	go func() {
		resp, err := server.Client().Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		clientDone <- err
	}()
	poll(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(dir, "notes.txt"))
		return strings.Contains(string(data), "started disconnect\n")
	})
	// When the client closes the connection, no release file is needed.
	cancel()
	if err := <-clientDone; err == nil {
		t.Fatal("cancelled request succeeded")
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not end after disconnect")
	}
}
