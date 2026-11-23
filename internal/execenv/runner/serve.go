package runner

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"

	"github.com/MichaelKinsy/PiG/durable/env"
	"github.com/coder/websocket"
	"github.com/sourcegraph/jsonrpc2"
)

var errProtocol = errors.New("unexpected machine call")

// Serve executes the calls that arrive on the socket until the socket closes. It owns the
// socket and the execution environment of the session.
func Serve(ctx context.Context, socket *websocket.Conn, local env.ExecutionEnv) error {
	return ServeWithInfo(ctx, socket, local, Ready{})
}

// ServeWithInfo executes calls on socket and reports extra version and self-update metadata in Ready.
func ServeWithInfo(ctx context.Context, socket *websocket.Conn, local env.ExecutionEnv, meta Ready) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() { _ = local.Cleanup(context.WithoutCancel(ctx)) }()
	m := &machine{local: local}
	conn := jsonrpc2.NewConn(ctx, NewStream(ctx, socket), m)
	defer func() { _ = conn.Close() }()
	home, _ := os.UserHomeDir()
	ready, readyCancel := context.WithTimeout(ctx, messageTimeout)
	defer readyCancel()
	info := Ready{
		OS:               runtime.GOOS,
		Arch:             runtime.GOARCH,
		Home:             home,
		Workspace:        local.Cwd(),
		Version:          meta.Version,
		CanSelfUpdate:    meta.CanSelfUpdate,
		SelfUpdateReason: meta.SelfUpdateReason,
	}
	if err := conn.Notify(ready, MethodReady, info); err != nil {
		return err
	}
	go heartbeat(ctx, socket, cancel)
	<-conn.DisconnectNotify()
	return nil
}

// machine answers the calls of one session.
type machine struct {
	local  env.ExecutionEnv
	mu     sync.Mutex
	tag    uint64
	cancel context.CancelFunc
}

func (m *machine) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var id Identity
	_ = decode(req.Params, &id)
	m.mu.Lock()
	if req.Notif {
		if req.Method == MethodCancel && m.cancel != nil && m.tag == id.Tag {
			m.cancel()
		}
		m.mu.Unlock()
		return
	}
	if m.cancel != nil {
		m.mu.Unlock()
		_ = conn.ReplyWithError(ctx, req.ID, machineError(ctx, errors.New("the machine is busy")))
		return
	}
	// Register the operation in the read loop before the next cancel can arrive.
	callCtx, stop := context.WithCancel(ctx)
	m.tag, m.cancel = id.Tag, stop
	m.mu.Unlock()
	go m.reply(callCtx, ctx, conn, req, id.Tag, stop)
}

func (m *machine) reply(callCtx, ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request, tag uint64, stop context.CancelFunc) {
	defer stop()
	value, err := m.run(callCtx, conn, req, req.Method, tag)
	m.mu.Lock()
	m.cancel = nil
	m.mu.Unlock()
	if err != nil {
		_ = conn.ReplyWithError(ctx, req.ID, machineError(callCtx, err))
		return
	}
	_ = conn.Reply(ctx, req.ID, value)
}

func (m *machine) run(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request, method string, tag uint64) (any, error) {
	switch method {
	case MethodExec:
		var params ExecParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		var command any = params.Shell
		if len(params.Argv) != 0 {
			command = params.Argv
		}
		result, err := m.local.Exec(ctx, command, &env.ShellExecOptions{
			Cwd: params.Cwd, Env: params.Env, InheritEnv: params.InheritEnv, Timeout: params.Timeout,
			OnOutput: func(outputCtx context.Context, text string, info env.ShellOutputInfo) {
				if outputCtx.Err() != nil {
					return
				}
				_ = conn.Notify(context.WithoutCancel(outputCtx), MethodOutput, OutputParams{
					Identity: IdentityOf(tag), Stream: string(info.Stream), Text: text,
				})
			},
		})
		if err != nil {
			return nil, err
		}
		return ExecResult{ExitCode: result.ExitCode}, nil
	case MethodRead:
		var params PathParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		reader, err := m.local.OpenBinaryReader(ctx, params.Path, &env.OpenBinaryReaderOptions{NoFollow: params.NoFollow})
		if err != nil {
			return nil, err
		}
		defer func() { _ = reader.Close(context.WithoutCancel(ctx)) }()
		info, err := reader.Info(ctx)
		if err != nil {
			return nil, err
		}
		if info.Size > maxFileBytes {
			return nil, errFileLimit
		}
		data, err := reader.Read(ctx, 0, info.Size)
		if err != nil {
			return nil, err
		}
		return DataResult{Data: data}, nil
	case MethodWrite, MethodAppend:
		var params WriteParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		if len(params.Data) > maxFileBytes {
			return nil, errFileLimit
		}
		if method == MethodWrite {
			return nil, m.local.WriteFile(ctx, params.Path, params.Data)
		}
		return nil, m.local.AppendFile(ctx, params.Path, params.Data)
	case MethodStat:
		var params PathParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		return m.local.FileInfo(ctx, params.Path)
	case MethodList:
		var params PathParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		return m.local.ListDir(ctx, params.Path)
	case MethodCanonical:
		var params PathParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		path, err := m.local.CanonicalPath(ctx, params.Path)
		return PathResult{Path: path}, err
	case MethodText:
		var params PathParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		text, err := m.local.ReadTextFile(ctx, params.Path)
		return TextResult{Text: text}, err
	case MethodMakeDir:
		var params MakeDirParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		return nil, m.local.CreateDir(ctx, params.Path, &env.CreateDirOptions{Recursive: params.Recursive})
	case MethodRemove:
		var params RemoveParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		return nil, m.local.Remove(ctx, params.Path, &env.RemoveOptions{Recursive: params.Recursive, Force: params.Force})
	case MethodRename:
		var params RenameParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		return nil, m.local.RenameFile(ctx, params.Path, params.Dest)
	case MethodTruncate:
		var params TruncateParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		return nil, m.local.TruncateFile(ctx, params.Path, params.Size)
	case MethodFlush:
		var params PathParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		return nil, m.local.FlushFile(ctx, params.Path)
	case MethodTempDir:
		var params TempDirParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		dir, err := m.local.CreateTempDir(ctx, params.Prefix)
		return PathResult{Path: dir}, err
	case MethodTempFile:
		var params TempFileParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		file, err := m.local.CreateTempFile(ctx, &env.CreateTempFileOptions{Prefix: value(params.Prefix), Suffix: value(params.Suffix)})
		return PathResult{Path: file}, err
	default:
		return nil, errProtocol
	}
}

// machineError turns an execution error into a JSON-RPC error that names its code.
func machineError(ctx context.Context, err error) *jsonrpc2.Error {
	if errors.Is(err, errProtocol) {
		return &jsonrpc2.Error{Code: jsonrpc2.CodeMethodNotFound, Message: err.Error()}
	}
	detail := Failure{Code: string(env.FileErrorUnknown)}
	if errors.Is(ctx.Err(), context.Canceled) {
		detail = Failure{Code: "canceled"}
	} else if failure, ok := errors.AsType[*env.FileError](err); ok {
		detail = Failure{Code: string(failure.Code), Path: failure.Path}
	}
	rpcErr := &jsonrpc2.Error{Code: CodeExecutionError, Message: err.Error()}
	rpcErr.SetError(detail)
	return rpcErr
}

func value[T any](pointer *T) T {
	var zero T
	if pointer == nil {
		return zero
	}
	return *pointer
}
