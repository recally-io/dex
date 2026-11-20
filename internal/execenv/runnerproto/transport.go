// Package runnerproto defines the WebSocket messages between Dex and a remote runner.
package runnerproto

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/coder/websocket"
)

const (
	Version       = "dex-agent.v1"
	MaxChunkBytes = 256 << 10
	// MaxMessageBytes includes JSON overhead and base64 expansion.
	MaxMessageBytes = 512 << 10
)

var (
	ErrVersion        = errors.New("unsupported runner protocol version")
	ErrInvalidMessage = errors.New("invalid runner message")
)

// Message carries one payload. IDs belong to one connection and are required
// for every message except Ready.
type Message struct {
	ID      uint64   `json:"id,omitzero"`
	Ready   *Ready   `json:"ready,omitzero"`
	Request *Request `json:"request,omitzero"`
	Chunk   *Chunk   `json:"chunk,omitzero"`
	Output  *Output  `json:"output,omitzero"`
	Result  *Result  `json:"result,omitzero"`
	Cancel  *Cancel  `json:"cancel,omitzero"`
}

type Ready struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Home      string `json:"home,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

type Request struct {
	Op      string   `json:"op"`
	Path    string   `json:"path,omitempty"`
	Command string   `json:"command,omitempty"`
	Argv    []string `json:"argv,omitempty"`
	Options []byte   `json:"options,omitempty"`
}

type Chunk struct {
	Data []byte `json:"data,omitempty"`
	End  bool   `json:"end,omitzero"`
}

type Output struct {
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

type Result struct {
	ExitCode  int      `json:"exitCode,omitzero"`
	SpillPath string   `json:"spillPath,omitempty"`
	Error     *Failure `json:"error,omitempty"`
}

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Cancel struct{}

// Transport sends one message per WebSocket text message. The caller owns the
// connection, authentication, and session state. Only one goroutine may read.
type Transport struct {
	socket *websocket.Conn
}

// New uses a socket that already negotiated the runner subprotocol.
func New(socket *websocket.Conn) (*Transport, error) {
	if socket.Subprotocol() != Version {
		return nil, ErrVersion
	}
	socket.SetReadLimit(MaxMessageBytes)
	return &Transport{socket: socket}, nil
}

func (t *Transport) Read(ctx context.Context) (Message, error) {
	kind, data, err := t.socket.Read(ctx)
	if err != nil {
		return Message{}, err
	}
	if kind != websocket.MessageText {
		return Message{}, ErrInvalidMessage
	}
	var msg Message
	if err := json.Unmarshal(data, &msg, json.RejectUnknownMembers(true)); err != nil {
		return Message{}, fmt.Errorf("%w: malformed JSON", ErrInvalidMessage)
	}
	if !msg.valid() {
		return Message{}, ErrInvalidMessage
	}
	return msg, nil
}

func (t *Transport) Write(ctx context.Context, msg Message) error {
	if !msg.valid() {
		return ErrInvalidMessage
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("%w: cannot encode payload", ErrInvalidMessage)
	}
	if len(data) > MaxMessageBytes {
		return ErrInvalidMessage
	}
	return t.socket.Write(ctx, websocket.MessageText, data)
}

func (m Message) valid() bool {
	if (m.Ready != nil) != (m.ID == 0) {
		return false
	}
	count := 0
	for _, present := range [...]bool{m.Ready != nil, m.Request != nil, m.Chunk != nil, m.Output != nil, m.Result != nil, m.Cancel != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return false
	}
	if ready := m.Ready; ready != nil {
		return ready.OS != "" && ready.Arch != ""
	}
	if request := m.Request; request != nil {
		switch request.Op {
		case "read", "write":
			return request.Path != "" && request.Command == ""
		case "exec":
			return request.Path == "" && (request.Command != "") != (len(request.Argv) != 0)
		case "append", "stat", "list", "canonical", "mkdir", "remove", "rename", "truncate", "flush", "temp-dir", "temp-file", "text":
			return request.Command == "" && len(request.Argv) == 0
		default:
			return false
		}
	}
	if chunk := m.Chunk; chunk != nil {
		return len(chunk.Data) <= MaxChunkBytes && (len(chunk.Data) != 0 || chunk.End)
	}
	if output := m.Output; output != nil {
		return output.Stream == "stdout" || output.Stream == "stderr"
	}
	return m.Result == nil || m.Result.Error == nil || m.Result.Error.Code != ""
}
