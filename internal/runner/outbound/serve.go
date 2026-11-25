package outbound

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
	m := &machine{local: local, active: make(map[uint64]context.CancelFunc)}
	conn := jsonrpc2.NewConn(ctx, NewStream(ctx, socket), m)
	defer func() {
		cancel()
		_ = conn.Close()
		// Prevent WaitGroup registration from racing the drain and cleanup.
		m.mu.Lock()
		m.draining = true
		m.mu.Unlock()
		m.calls.Wait()
		_ = local.Cleanup(context.WithoutCancel(ctx))
	}()
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
		MaxInFlight:      maxInFlight,
		StartEvents:      true,
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
	local    env.ExecutionEnv
	mu       sync.Mutex
	active   map[uint64]context.CancelFunc
	calls    sync.WaitGroup
	draining bool
}

func (m *machine) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	var id Identity
	if err := decode(req.Params, &id); err != nil || id.Tag == 0 {
		if !req.Notif {
			_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{Code: jsonrpc2.CodeInvalidParams, Message: "invalid call tag"})
		}
		return
	}
	m.mu.Lock()
	if req.Notif {
		if stop := m.active[id.Tag]; req.Method == MethodCancel && stop != nil {
			stop()
		}
		m.mu.Unlock()
		return
	}
	if m.draining || ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	if len(m.active) >= maxInFlight || m.active[id.Tag] != nil {
		m.mu.Unlock()
		_ = conn.ReplyWithError(ctx, req.ID, machineError(ctx, errors.New("the machine is busy or the call tag is already active")))
		return
	}
	// Register the operation in the read loop before the next cancel can arrive.
	callCtx, stop := context.WithCancel(ctx)
	m.active[id.Tag] = stop
	m.calls.Go(func() { m.reply(callCtx, ctx, conn, req, id.Tag, stop) })
	m.mu.Unlock()
}

func (m *machine) reply(callCtx, sessionCtx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request, tag uint64, stop context.CancelFunc) {
	defer stop()
	value, err := m.execute(callCtx, conn, req, tag)
	// Release the slot before writing the reply so Handle never blocks on network
	// I/O and still observes the freed slot if the peer sends another call at once.
	m.mu.Lock()
	delete(m.active, tag)
	m.mu.Unlock()
	if err != nil {
		err = conn.ReplyWithError(sessionCtx, req.ID, machineError(callCtx, err))
	} else {
		err = conn.Reply(sessionCtx, req.ID, value)
		if errors.Is(err, ErrMessageTooLarge) {
			err = conn.ReplyWithError(sessionCtx, req.ID, machineError(callCtx, err))
		}
	}
	if err != nil {
		// Without a final result the peer must treat the operation as unknown.
		_ = conn.Close()
	}
}

func (m *machine) execute(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request, tag uint64) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var wantStart bool
	if decode(req.Meta, &wantStart) == nil && wantStart {
		if err := conn.Notify(ctx, MethodStarted, IdentityOf(tag)); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return m.run(ctx, conn, req, tag)
}

func (m *machine) run(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request, tag uint64) (any, error) {
	switch req.Method {
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
				if err := conn.Notify(outputCtx, MethodOutput, OutputParams{
					Tag: tag, Stream: string(info.Stream), Text: text,
				}); err != nil && !errors.Is(err, ErrMessageTooLarge) {
					_ = conn.Close()
				}
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
		if req.Method == MethodWrite {
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
