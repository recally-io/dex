// Package mcptest provides a file-backed MCP fixture.
package mcptest

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"uuid"
)

type object = map[string]any

// Server serves tools that operate on files in Dir.
type Server struct {
	Dir      string
	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	mu     sync.Mutex
	calls  map[string]context.CancelFunc
	closed bool
}

func (p *session) cancel(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if id == "" {
		p.closed = true
	}
	for key, cancel := range p.calls {
		if id == "" || id == key {
			cancel()
		}
	}
}

func (p *session) start(ctx context.Context, id string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	if p.calls == nil {
		p.calls = make(map[string]context.CancelFunc)
	}
	p.calls[id] = cancel
	if p.closed {
		cancel()
	}
	p.mu.Unlock()
	return ctx, func() {
		cancel()
		p.mu.Lock()
		delete(p.calls, id)
		p.mu.Unlock()
	}
}

type request struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      jsontext.Value `json:"id,omitempty"`
	Method  string         `json:"method"`
	Params  jsontext.Value `json:"params,omitempty"`
}

func rpcError(id jsontext.Value, code int, message string) object {
	if len(id) == 0 {
		id = jsontext.Value("null")
	}
	return object{"jsonrpc": "2.0", "id": id, "error": object{"code": code, "message": message}}
}

func supported(v string) bool {
	return v == "2025-11-25" || v == "2025-06-18" || v == "2025-03-26"
}

func appendFile(dir, name, text string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	_, err = io.WriteString(f, text+"\n")
	return errors.Join(err, f.Close())
}

func (s *Server) dispatch(ctx context.Context, p *session, req request) object {
	s.mu.Lock()
	err := appendFile(s.Dir, "ops.log", req.Method)
	s.mu.Unlock()
	if err != nil {
		return rpcError(req.ID, -32603, err.Error())
	}
	if req.Method == "notifications/cancelled" {
		var params struct {
			RequestID jsontext.Value `json:"requestId"`
		}
		if json.Unmarshal(req.Params, &params) == nil {
			p.cancel(string(params.RequestID))
		}
	}
	if len(req.ID) == 0 {
		return nil
	}
	var result any
	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return rpcError(req.ID, -32602, err.Error())
		}
		if !supported(params.ProtocolVersion) {
			params.ProtocolVersion = "2025-06-18"
		}
		result = object{"protocolVersion": params.ProtocolVersion, "capabilities": object{"tools": object{"listChanged": false}}, "serverInfo": object{"name": "mcptest", "version": "1"}}
	case "ping":
		result = object{}
	case "tools/list":
		var params struct {
			Cursor string `json:"cursor"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return rpcError(req.ID, -32602, err.Error())
			}
		}
		offset := 0
		switch params.Cursor {
		case "":
		case cursor(2):
			offset = 2
		case cursor(4):
			offset = 4
		default:
			return rpcError(req.ID, -32602, "invalid cursor")
		}
		tools := toolList()
		end := min(offset+2, len(tools))
		page := object{"tools": tools[offset:end]}
		if end < len(tools) {
			page["nextCursor"] = cursor(end)
		}
		result = page
	case "tools/call":
		var params struct {
			Name      string                    `json:"name"`
			Arguments map[string]jsontext.Value `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return rpcError(req.ID, -32602, err.Error())
		}
		switch params.Name {
		case "append", "read", "fail", "slow_echo", "wait":
		default:
			return rpcError(req.ID, -32602, "unknown tool")
		}
		arg := ""
		key := "text"
		if params.Name == "wait" {
			key = "name"
		}
		if params.Name == "append" || params.Name == "slow_echo" || params.Name == "wait" {
			if err := json.Unmarshal(params.Arguments[key], &arg); err != nil || string(params.Arguments[key]) == "null" {
				return rpcError(req.ID, -32602, "expected string argument: "+key)
			}
		}
		text, err := s.call(ctx, params.Name, arg)
		result = object{"content": []object{{"type": "text", "text": text}}}
		if err != nil {
			result = object{"content": []object{{"type": "text", "text": err.Error()}}, "isError": true}
		}
	default:
		return rpcError(req.ID, -32601, "method not found")
	}
	return object{"jsonrpc": "2.0", "id": req.ID, "result": result}
}

func cursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("tools:%d", offset)))
}

func toolList() []object {
	tools := make([]object, 0, 5)
	for _, name := range []string{"append", "read", "fail", "slow_echo", "wait"} {
		schema := object{"type": "object", "properties": object{}}
		key := "text"
		if name == "wait" {
			key = "name"
		}
		if name == "append" || name == "slow_echo" || name == "wait" {
			schema["properties"] = object{key: object{"type": "string"}}
			schema["required"] = []string{key}
		}
		tools = append(tools, object{"name": name, "inputSchema": schema})
	}
	return tools
}

func (s *Server) call(ctx context.Context, name, arg string) (string, error) {
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	notes := "notes.txt"
	switch name {
	case "fail":
		return "", errors.New("failed on purpose")
	case "slow_echo":
		return "echo: " + arg, nil
	case "read":
		data, err := root.ReadFile(notes)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return string(data), err
	case "append":
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := appendFile(s.Dir, notes, arg); err != nil {
			return "", err
		}
		data, err := root.ReadFile(notes)
		return fmt.Sprintf("appended %d", strings.Count(string(data), "\n")), err
	case "wait":
		if !filepath.IsLocal(arg) || filepath.Base(arg) != arg || arg == "." {
			return "", errors.New("invalid release name")
		}
		s.mu.Lock()
		err := appendFile(s.Dir, notes, "started "+arg)
		s.mu.Unlock()
		if err != nil {
			return "", err
		}
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := root.Stat(arg + ".release"); err == nil {
				return "released " + arg, nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-ticker.C:
			}
		}
	}
	return "", errors.New("unknown tool")
}

// ServeStdio serves newline-delimited JSON-RPC until r ends.
func (s *Server) ServeStdio(r io.Reader, w io.Writer) error {
	p := new(session)
	var wg sync.WaitGroup
	var output sync.Mutex
	var writeErr error
	send := func(response object) {
		if response == nil {
			return
		}
		data, err := json.Marshal(response)
		output.Lock()
		defer output.Unlock()
		if err == nil {
			_, err = fmt.Fprintf(w, "%s\n", data)
		}
		writeErr = errors.Join(writeErr, err)
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			send(rpcError(nil, -32700, "parse error"))
			continue
		}
		if req.JSONRPC != "2.0" || req.Method == "" {
			send(rpcError(req.ID, -32600, "invalid request"))
			continue
		}
		if len(req.ID) == 0 {
			send(s.dispatch(context.Background(), p, req))
			continue
		}
		ctx, done := p.start(context.Background(), string(req.ID))
		wg.Go(func() { defer done(); send(s.dispatch(ctx, p, req)) })
	}
	p.cancel("")
	wg.Wait()
	return errors.Join(scanner.Err(), writeErr)
}

// ServeHTTP serves one MCP Streamable HTTP endpoint.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !supported(v) {
		http.Error(w, "unsupported protocol version", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "POST, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req request
	if r.Method == http.MethodPost {
		data, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil || json.Unmarshal(data, &req) != nil || req.JSONRPC != "2.0" || req.Method == "" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
	}
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = make(map[string]*session)
	}
	id := r.Header.Get("Mcp-Session-Id")
	p := s.sessions[id]
	if req.Method == "initialize" && id == "" {
		id = uuid.New().String()
		p = new(session)
		s.sessions[id] = p
		w.Header().Set("Mcp-Session-Id", id)
	}
	if r.Method == http.MethodDelete && p != nil {
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	if id == "" {
		http.Error(w, "missing session", http.StatusBadRequest)
		return
	}
	if p == nil {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodDelete {
		p.cancel("")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ctx, done := p.start(r.Context(), string(req.ID))
	defer done()
	response := s.dispatch(ctx, p, req)
	if response == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	data, err := json.Marshal(response)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var params struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(req.Params, &params)
	if req.Method == "tools/call" && params.Name == "slow_echo" && strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
	} else {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}
}
