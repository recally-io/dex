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
)

// Serve executes requests in the supplied environment until socket loss or shutdown.
// It owns the socket and the execution environment for that session.
func Serve(ctx context.Context, socket *websocket.Conn, local env.ExecutionEnv) error {
	ctx, cancel := context.WithCancel(ctx)
	defer func() { _ = local.Cleanup(context.WithoutCancel(ctx)) }()
	defer func() { _ = socket.CloseNow() }()
	defer cancel()
	wire, err := runnerproto.New(socket)
	if err != nil {
		return err
	}
	s := executor{wire: wire, local: local, cancel: cancel}
	defer func() { cancel(); s.work.Wait() }()
	home, _ := os.UserHomeDir()
	if err := write(ctx, wire, runnerproto.Message{Ready: &runnerproto.Ready{OS: runtime.GOOS, Arch: runtime.GOARCH, Home: home, Workspace: local.Cwd()}}); err != nil {
		return err
	}
	go heartbeat(ctx, socket, cancel)
	for {
		msg, err := wire.Read(ctx)
		if err != nil {
			return err
		}
		switch {
		case msg.Request != nil:
			err = s.start(ctx, msg.ID, *msg.Request)
		case msg.Chunk != nil:
			err = s.chunk(ctx, msg.ID, *msg.Chunk)
		case msg.Cancel != nil:
			s.stop(msg.ID)
		default:
			err = errProtocol
		}
		if err != nil {
			return err
		}
	}
}

type executor struct {
	wire   *runnerproto.Transport
	local  env.ExecutionEnv
	cancel context.CancelFunc
	work   sync.WaitGroup
	mu     sync.Mutex
	last   uint64
	active *operation
}

type operation struct {
	id         uint64
	request    runnerproto.Request
	ctx        context.Context
	stop       context.CancelFunc
	collecting bool
	data       []byte
}

func (s *executor) start(ctx context.Context, id uint64, request runnerproto.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id <= s.last || s.active != nil {
		return errProtocol
	}
	s.last = id
	runCtx, stop := context.WithCancel(ctx)
	op := &operation{id: id, request: request, ctx: runCtx, stop: stop, collecting: request.Op == "write" || request.Op == "append"}
	s.active = op
	if !op.collecting {
		s.work.Go(func() { s.run(ctx, op) })
	}
	return nil
}

func (s *executor) chunk(ctx context.Context, id uint64, chunk runnerproto.Chunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	op := s.active
	if op == nil || op.id != id || !op.collecting || len(op.data)+len(chunk.Data) > maxFileBytes {
		return errProtocol
	}
	op.data = append(op.data, chunk.Data...)
	if chunk.End {
		op.collecting = false
		s.work.Go(func() { s.run(ctx, op) })
	}
	return nil
}

func (s *executor) stop(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil && s.active.id == id {
		s.active.stop()
	}
}

func (s *executor) run(ctx context.Context, op *operation) {
	defer op.stop()
	result := new(runnerproto.Result)
	// The read loop stores the child context so it can cancel this operation by ID.
	err := s.execute(op.ctx, op, result) //nolint:contextcheck
	if err != nil {
		code := "failed"
		if errors.Is(op.ctx.Err(), context.Canceled) {
			code = "canceled"
		}
		if failure, ok := errors.AsType[*env.FileError](err); ok {
			code = string(failure.Code)
		}
		result.Error = &runnerproto.Failure{Code: code, Message: err.Error()}
	}
	s.mu.Lock()
	s.active = nil
	s.mu.Unlock()
	if err := write(ctx, s.wire, runnerproto.Message{ID: op.id, Result: result}); err != nil {
		s.cancel()
	}
}

func (s *executor) execute(ctx context.Context, op *operation, result *runnerproto.Result) error {
	switch op.request.Op {
	case "read":
		options, err := readOptions[*env.OpenBinaryReaderOptions](op.request)
		if err != nil {
			return err
		}
		reader, err := s.local.OpenBinaryReader(ctx, op.request.Path, options)
		if err != nil {
			return err
		}
		defer reader.Close(context.WithoutCancel(ctx))
		info, err := reader.Info(ctx)
		if err != nil {
			return err
		}
		if info.Size > maxFileBytes {
			return errFileLimit
		}
		if info.Size == 0 {
			return writeChunks(ctx, s.wire, op.id, nil)
		}
		for offset := int64(0); offset < info.Size; {
			data, err := reader.Read(ctx, offset, min(info.Size-offset, runnerproto.MaxChunkBytes))
			if err != nil {
				return err
			}
			offset += int64(len(data))
			end := offset >= info.Size || len(data) == 0
			if err := write(ctx, s.wire, runnerproto.Message{ID: op.id, Chunk: &runnerproto.Chunk{Data: data, End: end}}); err != nil {
				return err
			}
			if end {
				break
			}
		}
	case "write":
		return s.local.WriteFile(ctx, op.request.Path, op.data)
	case "append":
		return s.local.AppendFile(ctx, op.request.Path, op.data)
	case "exec":
		config, err := readOptions[CommandOptions](op.request)
		if err != nil {
			return err
		}
		var command any = op.request.Command
		if len(op.request.Argv) != 0 {
			command = op.request.Argv
		}
		ret, err := s.local.Exec(ctx, command, &env.ShellExecOptions{Cwd: config.Cwd, Env: config.Env, InheritEnv: config.InheritEnv, Timeout: config.Timeout, OnOutput: func(outputCtx context.Context, text string, info env.ShellOutputInfo) {
			if outputCtx.Err() != nil {
				return
			}
			if err := write(context.WithoutCancel(outputCtx), s.wire, runnerproto.Message{ID: op.id, Output: &runnerproto.Output{Stream: string(info.Stream), Text: text}}); err != nil {
				s.cancel()
			}
		}})
		result.ExitCode = ret.ExitCode
		return err
	default:
		return s.files(ctx, op)
	}
	return nil
}
