package runner

import (
	"context"
	"errors"
	"os"
	"runtime"
	"sync"

	"github.com/MichaelKinsy/PiG/durable/env"
	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/execenv/runnerproto"
	"github.com/sourcegraph/jsonrpc2"
)

var errProtocol = errors.New("unexpected machine call")

// Serve executes the calls that arrive on the socket until the socket closes. It owns the
// socket and the execution environment of the session.
func Serve(ctx context.Context, socket *websocket.Conn, local env.ExecutionEnv) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() { _ = local.Cleanup(context.WithoutCancel(ctx)) }()
	m := &machine{local: local, cancels: map[uint64]context.CancelFunc{}}
	conn := jsonrpc2.NewConn(ctx, runnerproto.NewStream(ctx, socket), jsonrpc2.AsyncHandler(m))
	defer func() { _ = conn.Close() }()
	home, _ := os.UserHomeDir()
	ready, readyCancel := context.WithTimeout(ctx, messageTimeout)
	defer readyCancel()
	info := runnerproto.Ready{OS: runtime.GOOS, Arch: runtime.GOARCH, Home: home, Workspace: local.Cwd()}
	if err := conn.Notify(ready, runnerproto.MethodReady, info); err != nil {
		return err
	}
	go heartbeat(ctx, socket, cancel)
	<-conn.DisconnectNotify()
	return nil
}

// machine answers the calls of one session.
type machine struct {
	local   env.ExecutionEnv
	mu      sync.Mutex
	cancels map[uint64]context.CancelFunc
}

func (m *machine) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var tag runnerproto.Identity
	_ = decode(req.Params, &tag)
	if req.Notif {
		if req.Method == runnerproto.MethodCancel {
			m.stop(tag.Tag)
		}
		return
	}
	callCtx, stop := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancels[tag.Tag] = stop
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.cancels, tag.Tag)
		m.mu.Unlock()
		stop()
	}()
	value, err := m.run(callCtx, conn, req, req.Method, tag.Tag)
	if err != nil {
		_ = conn.ReplyWithError(ctx, req.ID, machineError(callCtx, err))
		return
	}
	_ = conn.Reply(ctx, req.ID, value)
}

func (m *machine) stop(tag uint64) {
	m.mu.Lock()
	stop := m.cancels[tag]
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (m *machine) run(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request, method string, tag uint64) (any, error) {
	switch method {
	case runnerproto.MethodExec:
		var params runnerproto.ExecParams
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
				_ = conn.Notify(context.WithoutCancel(outputCtx), runnerproto.MethodOutput, runnerproto.OutputParams{
					Identity: runnerproto.IdentityOf(tag), Stream: string(info.Stream), Text: text,
				})
			},
		})
		if err != nil {
			return nil, err
		}
		return runnerproto.ExecResult{ExitCode: result.ExitCode}, nil
	case runnerproto.MethodRead:
		var params runnerproto.PathParams
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
		return runnerproto.DataResult{Data: data}, nil
	case runnerproto.MethodWrite, runnerproto.MethodAppend:
		var params runnerproto.WriteParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		if len(params.Data) > maxFileBytes {
			return nil, errFileLimit
		}
		if method == runnerproto.MethodWrite {
			return nil, m.local.WriteFile(ctx, params.Path, params.Data)
		}
		return nil, m.local.AppendFile(ctx, params.Path, params.Data)
	case runnerproto.MethodStat:
		var params runnerproto.PathParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		return m.local.FileInfo(ctx, params.Path)
	case runnerproto.MethodList:
		var params runnerproto.PathParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		return m.local.ListDir(ctx, params.Path)
	case runnerproto.MethodCanonical:
		var params runnerproto.PathParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		path, err := m.local.CanonicalPath(ctx, params.Path)
		return runnerproto.PathResult{Path: path}, err
	case runnerproto.MethodText:
		var params runnerproto.PathParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		text, err := m.local.ReadTextFile(ctx, params.Path)
		return runnerproto.TextResult{Text: text}, err
	case runnerproto.MethodMakeDir:
		var params runnerproto.MakeDirParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		return nil, m.local.CreateDir(ctx, params.Path, &env.CreateDirOptions{Recursive: params.Recursive})
	case runnerproto.MethodRemove:
		var params runnerproto.RemoveParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		return nil, m.local.Remove(ctx, params.Path, &env.RemoveOptions{Recursive: params.Recursive, Force: params.Force})
	case runnerproto.MethodRename:
		var params runnerproto.RenameParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		return nil, m.local.RenameFile(ctx, params.Path, params.Dest)
	case runnerproto.MethodTruncate:
		var params runnerproto.TruncateParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		return nil, m.local.TruncateFile(ctx, params.Path, params.Size)
	case runnerproto.MethodFlush:
		var params runnerproto.PathParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		return nil, m.local.FlushFile(ctx, params.Path)
	case runnerproto.MethodTempDir:
		var params runnerproto.TempDirParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		dir, err := m.local.CreateTempDir(ctx, params.Prefix)
		return runnerproto.PathResult{Path: dir}, err
	case runnerproto.MethodTempFile:
		var params runnerproto.TempFileParams
		if err := decode(req.Params, &params); err != nil {
			return nil, err
		}
		file, err := m.local.CreateTempFile(ctx, &env.CreateTempFileOptions{Prefix: value(params.Prefix), Suffix: value(params.Suffix)})
		return runnerproto.PathResult{Path: file}, err
	default:
		return nil, errProtocol
	}
}

// machineError turns an execution error into a JSON-RPC error that names its code.
func machineError(ctx context.Context, err error) *jsonrpc2.Error {
	if errors.Is(err, errProtocol) {
		return &jsonrpc2.Error{Code: jsonrpc2.CodeMethodNotFound, Message: err.Error()}
	}
	detail := runnerproto.Failure{Code: string(env.FileErrorUnknown)}
	if errors.Is(ctx.Err(), context.Canceled) {
		detail = runnerproto.Failure{Code: "canceled"}
	} else if failure, ok := errors.AsType[*env.FileError](err); ok {
		detail = runnerproto.Failure{Code: string(failure.Code), Path: failure.Path}
	}
	rpcErr := &jsonrpc2.Error{Code: runnerproto.CodeExecutionError, Message: err.Error()}
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
