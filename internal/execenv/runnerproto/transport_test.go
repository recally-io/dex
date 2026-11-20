package runnerproto_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/execenv/runnerproto"
)

func TestRunnerMessageOverTLS(t *testing.T) {
	controller, runner := sockets(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for _, tc := range []struct {
		from *runnerproto.Transport
		to   *runnerproto.Transport
		msg  runnerproto.Message
	}{
		{runner, controller, runnerproto.Message{Ready: &runnerproto.Ready{OS: "linux", Arch: "arm64"}}},
		{controller, runner, runnerproto.Message{ID: 1, Request: &runnerproto.Request{Op: "write", Path: "file"}}},
		{controller, runner, runnerproto.Message{ID: 1, Chunk: &runnerproto.Chunk{Data: bytes.Repeat([]byte{0, 0xff, '\n', '\r'}, runnerproto.MaxChunkBytes/4), End: true}}},
		{runner, controller, runnerproto.Message{ID: 1, Result: new(runnerproto.Result)}},
		{controller, runner, runnerproto.Message{ID: 2, Request: &runnerproto.Request{Op: "exec", Command: "echo hello"}}},
		{runner, controller, runnerproto.Message{ID: 2, Output: &runnerproto.Output{Stream: "stderr", Text: "hello\n"}}},
		{controller, runner, runnerproto.Message{ID: 2, Cancel: new(runnerproto.Cancel)}},
		{runner, controller, runnerproto.Message{ID: 2, Result: &runnerproto.Result{Error: &runnerproto.Failure{Code: "canceled", Message: "command stopped"}}}},
	} {
		if err := tc.from.Write(ctx, tc.msg); err != nil {
			t.Fatal(err)
		}
		got, err := tc.to.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, tc.msg) {
			t.Fatalf("message did not arrive intact: got %+v, want %+v", got, tc.msg)
		}
	}
}

func TestRunnerRejectsInvalidMessages(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
	}{
		{"two payloads", `{"id":1,"cancel":{},"result":{}}`},
		{"missing ID", `{"cancel":{}}`},
		{"unknown field", `{"id":1,"cancel":{"typo":true}}`},
		{"duplicate JSON member", `{"id":1,"id":2,"cancel":{}}`},
		{"oversized message", `{"id":1,"output":{"stream":"stdout","text":"` + strings.Repeat("a", runnerproto.MaxMessageBytes) + `"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caught := make(chan *runnerproto.Transport, 1)
			s := newServer(t, caught)
			peer := dial(t, s, runnerproto.Version)
			transport := accepted(t, caught)
			done := make(chan error, 1)
			go func() {
				_, err := transport.Read(t.Context())
				done <- err
			}()
			if err := peer.Write(t.Context(), websocket.MessageText, []byte(tc.data)); err != nil && tc.name != "oversized message" {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if tc.name == "oversized message" {
					if err == nil {
						t.Fatal("accepted oversized message")
					}
				} else if !errors.Is(err, runnerproto.ErrInvalidMessage) {
					t.Fatalf("got %v, want invalid message", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("read did not finish")
			}
		})
	}
}

func TestRunnerRejectsUnnegotiatedVersion(t *testing.T) {
	caught := make(chan *runnerproto.Transport, 1)
	s := newServer(t, caught)
	_ = dial(t, s, "unsupported")
	if transport := accepted(t, caught); transport != nil {
		t.Fatal("accepted incompatible peer")
	}
}

func sockets(t *testing.T) (*runnerproto.Transport, *runnerproto.Transport) {
	t.Helper()
	caught := make(chan *runnerproto.Transport, 1)
	s := newServer(t, caught)
	peer, err := runnerproto.New(dial(t, s, runnerproto.Version))
	if err != nil {
		t.Fatal(err)
	}
	server := accepted(t, caught)
	if server == nil {
		t.Fatal("server did not negotiate protocol")
	}
	return server, peer
}

func newServer(t *testing.T, caught chan<- *runnerproto.Transport) *httptest.Server {
	t.Helper()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{runnerproto.Version}})
		if err != nil {
			t.Error(err)
			return
		}
		t.Cleanup(func() { _ = c.CloseNow() })
		transport, err := runnerproto.New(c)
		if err != nil && !errors.Is(err, runnerproto.ErrVersion) {
			t.Error(err)
		}
		caught <- transport
	}))
	t.Cleanup(s.Close)
	return s
}

func dial(t *testing.T, s *httptest.Server, version string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// websocket.Dial clears the successful HTTP response body after taking ownership of the socket.
	c, _, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(s.URL, "https"), &websocket.DialOptions{HTTPClient: s.Client(), Subprotocols: []string{version}}) //nolint:bodyclose
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

func accepted(t *testing.T, caught <-chan *runnerproto.Transport) *runnerproto.Transport {
	t.Helper()
	select {
	case transport := <-caught:
		return transport
	case <-time.After(5 * time.Second):
		t.Fatal("server did not accept")
		return nil
	}
}
