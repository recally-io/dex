// Package runnerproto defines the JSON-RPC messages between Dex and a remote machine.
package runnerproto

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"sync"

	"github.com/coder/websocket"
)

const (
	// Version is the WebSocket subprotocol that both sides negotiate before the stream.
	Version = "dex-agent.v1"
	// MaxMessageBytes bounds one JSON-RPC message. A 16 MiB file grows about a third in base64.
	MaxMessageBytes = 24 << 20

	// MethodReady is the first message from the machine.
	MethodReady = "ready"
	// MethodOutput carries one chunk of command output while exec runs.
	MethodOutput = "output"
	// MethodCancel stops the call with the same tag.
	MethodCancel = "cancel"

	MethodExec      = "exec"
	MethodRead      = "read"
	MethodWrite     = "write"
	MethodAppend    = "append"
	MethodStat      = "stat"
	MethodList      = "list"
	MethodCanonical = "canonical"
	MethodText      = "text"
	MethodMakeDir   = "mkdir"
	MethodRemove    = "remove"
	MethodRename    = "rename"
	MethodTruncate  = "truncate"
	MethodFlush     = "flush"
	MethodTempDir   = "temp-dir"
	MethodTempFile  = "temp-file"
)

// Ready is the platform and the workspace of the machine.
type Ready struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Home      string `json:"home,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

// Identity names one call, so a cancel notification can stop it and so output can follow
// its call. Every params message embeds it.
type Identity struct {
	Tag uint64 `json:"tag,omitzero"`
}

// ExecParams carries one command. Exactly one of Shell and Argv is set.
type ExecParams struct {
	Identity
	Shell      string            `json:"shell,omitempty"`
	Argv       []string          `json:"argv,omitempty"`
	Cwd        string            `json:"cwd,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	InheritEnv *bool             `json:"inheritEnv,omitzero"`
	// Timeout is in seconds; absent means no timeout.
	Timeout *float64 `json:"timeout,omitzero"`
}

// ExecResult carries the exit code of a finished command.
type ExecResult struct {
	ExitCode int `json:"exitCode,omitzero"`
}

// OutputParams carries command output. Stream is stdout or stderr.
type OutputParams struct {
	Identity
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

// CancelParams stops the call with the same tag.
type CancelParams struct {
	Identity
}

// PathParams addresses one path. NoFollow refuses a symbolic link as the final component.
type PathParams struct {
	Identity
	Path     string `json:"path"`
	NoFollow bool   `json:"noFollow,omitzero"`
}

// DataResult carries file bytes.
type DataResult struct {
	Data []byte `json:"data"`
}

// WriteParams carries file bytes to write or to append.
type WriteParams struct {
	Identity
	Path string `json:"path"`
	Data []byte `json:"data"`
}

// TextResult carries decoded file text.
type TextResult struct {
	Text string `json:"text"`
}

// PathResult carries the path that the machine answered.
type PathResult struct {
	Path string `json:"path"`
}

// MakeDirParams creates one directory. Recursive is nil when the caller wants the default.
type MakeDirParams struct {
	Identity
	Path      string `json:"path"`
	Recursive *bool  `json:"recursive,omitzero"`
}

// RemoveParams removes one path.
type RemoveParams struct {
	Identity
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitzero"`
	Force     bool   `json:"force,omitzero"`
}

// RenameParams moves one path.
type RenameParams struct {
	Identity
	Path string `json:"path"`
	Dest string `json:"dest"`
}

// TruncateParams sets the size of one file.
type TruncateParams struct {
	Identity
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// TempDirParams creates a temporary directory.
type TempDirParams struct {
	Identity
	Prefix *string `json:"prefix,omitzero"`
}

// TempFileParams creates a temporary file.
type TempFileParams struct {
	Identity
	Prefix *string `json:"prefix,omitzero"`
	Suffix *string `json:"suffix,omitzero"`
}

// Stream adapts a WebSocket to jsonrpc2.ObjectStream: one JSON object per text message.
type Stream struct {
	ctx    context.Context
	socket *websocket.Conn
	mu     sync.Mutex
}

// NewStream takes ownership of a socket that already negotiated Version.
func NewStream(ctx context.Context, socket *websocket.Conn) *Stream {
	socket.SetReadLimit(MaxMessageBytes)
	return &Stream{ctx: ctx, socket: socket}
}

func (s *Stream) ReadObject(value any) error {
	_, data, err := s.socket.Read(s.ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func (s *Stream) WriteObject(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > MaxMessageBytes {
		return ErrMessageTooLarge
	}
	// The socket accepts one writer at a time.
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.socket.Write(s.ctx, websocket.MessageText, data)
}

func (s *Stream) Close() error {
	return s.socket.Close(websocket.StatusNormalClosure, "")
}

// Tagger gives each call a tag, so a cancel notification can name it.
type Tagger interface {
	SetTag(id uint64)
	CallTag() uint64
}

// IdentityOf names one call with its tag.
func IdentityOf(tag uint64) Identity { return Identity{Tag: tag} }

// SetTag names the call. Callers do not set it.
func (i *Identity) SetTag(id uint64) { i.Tag = id }

// CallTag returns the tag of the call.
func (i Identity) CallTag() uint64 { return i.Tag }

// CodeExecutionError marks a machine error that carries a Failure payload.
const CodeExecutionError = -32000

// Failure is the data of a machine error: an execution environment code and the path.
type Failure struct {
	Code string `json:"code"`
	Path string `json:"path,omitempty"`
}

// ErrMessageTooLarge refuses a message that exceeds MaxMessageBytes.
var ErrMessageTooLarge = errors.New("runner message exceeds the session limit")
