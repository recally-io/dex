package execenv

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"path"
	"strings"
	"sync"

	env "github.com/MichaelKinsy/PiG/durable/env"
	"github.com/recally-io/dex/internal/execenv/runner"
	"github.com/recally-io/dex/internal/execenv/runnerproto"
)

type runnerHandle struct {
	id     string
	client *runner.Client
}

func (h *runnerHandle) Kind() Kind                           { return KindRunner }
func (h *runnerHandle) Workspace() string                    { return h.client.Info().Workspace }
func (h *runnerHandle) Home(context.Context) (string, error) { return h.client.Info().Home, nil }
func (h *runnerHandle) Close(context.Context) error          { h.client.Close(); return nil }
func (h *runnerHandle) Env(_ context.Context, cwd string, extra map[string]string) (env.ExecutionEnv, error) {
	e := &runnerEnv{handle: h, extra: extra}
	e.SetCwd(cwd)
	return e, nil
}

type runnerEnv struct {
	handle *runnerHandle
	extra  map[string]string
	mu     sync.RWMutex
	cwd    string
}

var _ env.ExecutionEnv = (*runnerEnv)(nil)

func (e *runnerEnv) Id() string        { return "runner:" + e.handle.id } //nolint:revive // PiG FileSystem method.
func (e *runnerEnv) Cwd() string       { e.mu.RLock(); defer e.mu.RUnlock(); return e.cwd }
func (e *runnerEnv) SetCwd(cwd string) { cwd = e.resolve(cwd); e.mu.Lock(); e.cwd = cwd; e.mu.Unlock() }
func (e *runnerEnv) resolve(p string) string {
	cwd := e.Cwd()
	if cwd == "" {
		cwd = e.handle.Workspace()
	}
	home := e.handle.client.Info().Home
	if p == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return path.Join(home, rest)
	}
	if path.IsAbs(p) {
		return path.Clean(p)
	}
	return path.Join(cwd, p)
}
func (e *runnerEnv) AbsolutePath(ctx context.Context, p string) (string, error) {
	return e.resolve(p), ctx.Err()
}
func (e *runnerEnv) JoinPath(ctx context.Context, p []string) (string, error) {
	return path.Join(p...), ctx.Err()
}
func (e *runnerEnv) request(ctx context.Context, op, p string, opts any, input []byte) ([]byte, error) {
	options, err := encodeRunnerOptions(opts)
	if err != nil {
		return nil, err
	}
	return e.handle.client.FileOperation(ctx, runnerproto.Request{Op: op, Path: e.resolve(p), Options: options}, input)
}
func encodeRunnerOptions(opts any) ([]byte, error) {
	if opts == nil {
		return nil, nil
	}
	return json.Marshal(opts)
}
func runnerValue[T any](ctx context.Context, e *runnerEnv, op, p string, opts any) (T, error) {
	var value T
	data, err := e.request(ctx, op, p, opts, nil)
	if err != nil {
		return value, err
	}
	return value, json.Unmarshal(data, &value)
}
func (e *runnerEnv) ReadBinaryFile(ctx context.Context, p string) ([]byte, error) {
	return e.request(ctx, "read", p, nil, nil)
}
func (e *runnerEnv) ReadTextFile(ctx context.Context, p string) (string, error) {
	return runnerValue[string](ctx, e, "text", p, nil)
}
func (e *runnerEnv) FileInfo(ctx context.Context, p string) (env.FileInfo, error) {
	return runnerValue[env.FileInfo](ctx, e, "stat", p, nil)
}
func (e *runnerEnv) CanonicalPath(ctx context.Context, p string) (string, error) {
	return runnerValue[string](ctx, e, "canonical", p, nil)
}
func (e *runnerEnv) ListDir(ctx context.Context, p string) ([]env.FileInfo, error) {
	return runnerValue[[]env.FileInfo](ctx, e, "list", p, nil)
}
func (e *runnerEnv) Exists(ctx context.Context, p string) (bool, error) {
	_, err := e.FileInfo(ctx, p)
	if failure, ok := errors.AsType[*env.FileError](err); ok && failure.Code == env.FileErrorNotFound {
		return false, nil
	}
	return err == nil, err
}
func (e *runnerEnv) mutate(ctx context.Context, op, p string, opts any) error {
	_, err := e.request(ctx, op, p, opts, nil)
	return err
}
func (e *runnerEnv) CreateDir(ctx context.Context, p string, opts *env.CreateDirOptions) error {
	return e.mutate(ctx, "mkdir", p, opts)
}
func (e *runnerEnv) Remove(ctx context.Context, p string, opts *env.RemoveOptions) error {
	return e.mutate(ctx, "remove", p, opts)
}
func (e *runnerEnv) RenameFile(ctx context.Context, from, to string) error {
	return e.mutate(ctx, "rename", from, e.resolve(to))
}
func (e *runnerEnv) TruncateFile(ctx context.Context, p string, size int64) error {
	return e.mutate(ctx, "truncate", p, size)
}
func (e *runnerEnv) FlushFile(ctx context.Context, p string) error {
	return e.mutate(ctx, "flush", p, nil)
}
func (e *runnerEnv) CreateTempDir(ctx context.Context, prefix *string) (string, error) {
	return runnerValue[string](ctx, e, "temp-dir", "", prefix)
}
func (e *runnerEnv) CreateTempFile(ctx context.Context, opts *env.CreateTempFileOptions) (string, error) {
	return runnerValue[string](ctx, e, "temp-file", "", opts)
}
func (e *runnerEnv) write(ctx context.Context, op, p string, content any) error {
	var data []byte
	switch v := content.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return env.NewFileError(env.FileErrorInvalid, "expected string or bytes", p, nil)
	}
	_, err := e.request(ctx, op, p, nil, data)
	return err
}
func (e *runnerEnv) WriteFile(ctx context.Context, p string, v any) error {
	return e.write(ctx, "write", p, v)
}
func (e *runnerEnv) AppendFile(ctx context.Context, p string, v any) error {
	return e.write(ctx, "append", p, v)
}
func (e *runnerEnv) OpenDirReader(ctx context.Context, p string) (env.DirReader, error) {
	entries, err := e.ListDir(ctx, p)
	if err != nil {
		return nil, err
	}
	return &sliceDirReader{entries: entries}, nil
}
func (e *runnerEnv) Watch(context.Context, []env.WatchTarget, func(env.FileChange)) (env.FileWatcher, error) {
	return nil, env.NewFileError(env.FileErrorNotSupported, "this machine does not support file watches", e.Cwd(), nil)
}
func (e *runnerEnv) ListFiles(context.Context, string, *env.ListFilesOptions) (env.FileListing, error) {
	return env.FileListing{}, env.NewFileError(env.FileErrorNotSupported, "use directory traversal", e.Cwd(), nil)
}
func (e *runnerEnv) ReadTextLines(ctx context.Context, p string, opts *env.ReadTextLinesOptions) ([]string, error) {
	r, err := e.OpenTextLineReader(ctx, p)
	if err != nil {
		return nil, err
	}
	defer r.Close(ctx)
	var lines []string
	for opts == nil || opts.MaxLines == nil || len(lines) < *opts.MaxLines {
		line, err := r.ReadLine(ctx)
		if err != nil {
			return nil, err
		}
		if line == nil {
			break
		}
		lines = append(lines, line.Text)
	}
	return lines, nil
}
func (e *runnerEnv) OpenTextLineReader(ctx context.Context, p string) (env.TextLineReader, error) {
	text, err := e.ReadTextFile(ctx, p)
	if err != nil {
		return nil, err
	}
	return &runnerText{remaining: text}, nil
}
func (e *runnerEnv) OpenBinaryReader(ctx context.Context, p string, opts *env.OpenBinaryReaderOptions) (env.BinaryReader, error) {
	info, err := e.FileInfo(ctx, p)
	if err != nil {
		return nil, err
	}
	data, err := e.request(ctx, "read", p, opts, nil)
	if err != nil {
		return nil, err
	}
	info.Size = int64(len(data))
	return &runnerBinary{data: data, info: info}, nil
}
func (e *runnerEnv) Exec(ctx context.Context, command any, opts *env.ShellExecOptions) (env.ShellExecResult, error) {
	if opts == nil {
		opts = &env.ShellExecOptions{}
	}
	cwd := e.resolve(opts.Cwd)
	config := runner.CommandOptions{Cwd: cwd, InheritEnv: opts.InheritEnv, Timeout: opts.Timeout, Env: map[string]string{}}
	maps.Copy(config.Env, e.extra)
	maps.Copy(config.Env, opts.Env)
	serialized, err := json.Marshal(config)
	if err != nil {
		return env.ShellExecResult{}, err
	}
	request := runnerproto.Request{Op: "exec", Options: serialized}
	switch value := command.(type) {
	case string:
		request.Command = value
	case []string:
		request.Argv = value
	default:
		return env.ShellExecResult{}, fmt.Errorf("invalid command type %T", command)
	}
	code, err := e.handle.client.RunCommand(ctx, request, func(text string, stream env.ShellStream) {
		if opts.OnOutput != nil {
			opts.OnOutput(ctx, text, env.ShellOutputInfo{Stream: stream})
		}
	})
	return env.ShellExecResult{ExitCode: code}, err
}
func (e *runnerEnv) Cleanup(context.Context) error { e.handle.client.Close(); return nil }

type runnerText struct{ remaining string }

func (r *runnerText) ReadLine(ctx context.Context) (*env.TextLine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.remaining == "" {
		return nil, nil
	}
	line, rest, found := strings.Cut(r.remaining, "\n")
	r.remaining = rest
	if found {
		line = strings.TrimSuffix(line, "\r")
	}
	return &env.TextLine{Text: line, Terminated: found}, nil
}
func (r *runnerText) Close(context.Context) error { r.remaining = ""; return nil }

type runnerBinary struct {
	data   []byte
	info   env.FileInfo
	closed bool
}

func (r *runnerBinary) Info(ctx context.Context) (env.FileInfo, error) {
	if r.closed {
		return env.FileInfo{}, env.NewFileError(env.FileErrorInvalid, "reader closed", r.info.Path, nil)
	}
	return r.info, ctx.Err()
}
func (r *runnerBinary) Close(context.Context) error { r.closed = true; r.data = nil; return nil }
func (r *runnerBinary) Read(ctx context.Context, offset, length int64) ([]byte, error) {
	if _, err := r.Info(ctx); err != nil {
		return nil, err
	}
	if offset < 0 || length < 0 || offset > 1<<53-1 || length > 1<<53-1 {
		return nil, env.NewFileError(env.FileErrorInvalid, "invalid read range", r.info.Path, nil)
	}
	offset = min(offset, int64(len(r.data)))
	return bytes.Clone(r.data[offset:min(offset+length, int64(len(r.data)))]), nil
}
func (r *runnerBinary) ScanLines(ctx context.Context, opts env.ScanLinesOptions) (env.LineScan, error) {
	if _, err := r.Info(ctx); err != nil {
		return env.LineScan{}, err
	}
	scanner, err := env.NewLineScanner(opts.StartLine, opts.EndLine)
	if err != nil {
		return env.LineScan{}, env.NewFileError(env.FileErrorInvalid, "invalid line range", r.info.Path, nil)
	}
	scanner.Push(r.data)
	return scanner.Finish(), ctx.Err()
}
