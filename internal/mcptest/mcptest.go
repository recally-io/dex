// Package mcptest is an MCP server for tests. Its tools write to notes.txt in Dir, so a test can
// see which server got a call.
package mcptest

import (
	"bufio"
	"context"
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
)

type object = map[string]any

// Server serves the tools append {text}, read, fail and wait {name}. wait writes "started <name>"
// and returns when the file <name>.release exists in Dir.
type Server struct {
	Dir string
	// Prefix starts each tool name, so that two servers can have tools with the same full name.
	Prefix string
	// Twice lists the read tool twice, as a server with a bug can.
	Twice bool

	mu sync.Mutex
}

type request struct {
	ID     jsontext.Value `json:"id,omitempty"`
	Method string         `json:"method"`
	Params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"params"`
}

// ServeStdio serves newline-delimited JSON-RPC until r ends. The end of r cancels the calls.
func (s *Server) ServeStdio(r io.Reader, w io.Writer) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	var out sync.Mutex
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil || len(req.ID) == 0 {
			continue // notifications need no answer
		}
		// A wait call blocks, so each call has its own goroutine.
		wg.Go(func() {
			data, _ := json.Marshal(s.answer(ctx, req))
			out.Lock()
			defer out.Unlock()
			_, _ = fmt.Fprintf(w, "%s\n", data)
		})
	}
	cancel()
	wg.Wait()
	return scanner.Err()
}

// ServeHTTP serves MCP Streamable HTTP, with JSON answers and without sessions.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req request
	if err := json.UnmarshalRead(r.Body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, s.answer(r.Context(), req))
}

func (s *Server) answer(ctx context.Context, req request) object {
	var result object
	switch req.Method {
	case "initialize":
		result = object{
			"protocolVersion": "2025-06-18",
			"capabilities":    object{"tools": object{}},
			"serverInfo":      object{"name": "mcptest", "version": "1"},
		}
	case "tools/list":
		names := []string{"append", "read", "fail", "wait"}
		if s.Twice {
			names = append(names, "read")
		}
		tools := make([]object, 0, len(names))
		for _, name := range names {
			tools = append(tools, object{"name": s.Prefix + name, "inputSchema": object{"type": "object"}})
		}
		result = object{"tools": tools}
	case "tools/call":
		name, _ := strings.CutPrefix(req.Params.Name, s.Prefix)
		arg := fmt.Sprint(req.Params.Arguments["text"], req.Params.Arguments["name"])
		text, err := s.call(ctx, name, strings.ReplaceAll(arg, "<nil>", ""))
		result = object{"content": []object{{"type": "text", "text": text}}}
		if err != nil {
			result = object{"content": []object{{"type": "text", "text": err.Error()}}, "isError": true}
		}
	default:
		return object{"jsonrpc": "2.0", "id": req.ID, "error": object{"code": -32601, "message": "method not found"}}
	}
	return object{"jsonrpc": "2.0", "id": req.ID, "result": result}
}

func (s *Server) call(ctx context.Context, name, arg string) (string, error) {
	notes := filepath.Join(s.Dir, "notes.txt")
	switch name {
	case "append":
		if err := s.note(arg); err != nil {
			return "", err
		}
		data, err := os.ReadFile(notes) //nolint:gosec // Dir comes from the test
		return fmt.Sprintf("appended %d", strings.Count(string(data), "\n")), err
	case "read":
		data, err := os.ReadFile(notes) //nolint:gosec // Dir comes from the test
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return string(data), err
	case "fail":
		return "", errors.New("failed on purpose")
	case "wait":
		if err := s.note("started " + arg); err != nil {
			return "", err
		}
		for {
			if _, err := os.Stat(filepath.Join(s.Dir, filepath.Base(arg)+".release")); err == nil {
				return "released " + arg, nil
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	return "", errors.New("unknown tool")
}

func (s *Server) note(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(s.Dir, "notes.txt"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = io.WriteString(f, text+"\n")
	return errors.Join(err, f.Close())
}
