package execenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"strings"
	"sync"

	env "github.com/MichaelKinsy/PiG/durable/env"
	"github.com/recally-io/dex/internal/execenv/runner"
)

// runnerBackend opens leases on the live session of an enrolled machine.
type runnerBackend struct{ runners *Runners }

func (runnerBackend) normalize(spec EnvironmentSpec) (string, error) {
	if spec.Secret != "" {
		return "", fmt.Errorf("%w: a remote machine has no secret", ErrInvalidConfig)
	}
	return "{}", nil
}

func (b runnerBackend) probe(_ context.Context, _ *EnvironmentSpec, spec EnvironmentSpec) (TestResult, error) {
	session := b.runners.online(spec.ID)
	if session == nil {
		return TestResult{Error: "Runner is offline"}, nil
	}
	meta := session.Info()
	return TestResult{OK: true, Platform: meta.OS, Arch: meta.Arch, Home: meta.Home}, nil
}

func (b runnerBackend) open(ctx context.Context, agentID string, spec EnvironmentSpec) (lease, error) {
	session := b.runners.online(spec.ID)
	if session == nil {
		return nil, runner.ErrOffline
	}
	base := path.Join(path.Dir(session.Info().Workspace), "workspaces")
	h := &runnerHandle{id: spec.ID, client: session, workspace: remoteBotWorkspace(base, "/", agentID)}
	if err := createRemoteBotWorkspace(ctx, h); err != nil {
		return nil, fmt.Errorf("create runner workspace: %w", err)
	}
	return h, nil
}

func (b runnerBackend) changed(ctx context.Context, old EnvironmentSpec, cur *EnvironmentSpec) error {
	if cur != nil {
		return nil
	}
	return b.runners.Revoke(ctx, old.ID)
}

// release keeps the machine workspace: the machine belongs to the user.
func (runnerBackend) release(context.Context, string, EnvironmentSpec, string) error { return nil }
func (runnerBackend) disposable() bool                                               { return false }
func (b runnerBackend) generation(envID string) uint64                               { return b.runners.generation(envID) }

type runnerHandle struct {
	id        string
	client    *runner.Client
	workspace string
}

func (h *runnerHandle) Kind() Kind        { return KindRunner }
func (h *runnerHandle) Workspace() string { return h.workspace }
func (h *runnerHandle) Home() string      { return h.client.Info().Home }

// Close releases the lease. Runners owns the connection.
func (h *runnerHandle) Close(context.Context) error { return nil }
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

// call runs one method on the machine.
func (e *runnerEnv) call(ctx context.Context, method string, params runner.Tagger, result any) error {
	return e.handle.client.Call(ctx, method, params, result)
}
func runnerValue[T any, P runner.Tagger](ctx context.Context, e *runnerEnv, method string, params P) (T, error) {
	var value T
	err := e.call(ctx, method, params, &value)
	return value, err
}
func (e *runnerEnv) ReadBinaryFile(ctx context.Context, p string) ([]byte, error) {
	var result runner.DataResult
	err := e.call(ctx, runner.MethodRead, &runner.PathParams{Path: e.resolve(p)}, &result)
	return result.Data, err
}
func (e *runnerEnv) ReadTextFile(ctx context.Context, p string) (string, error) {
	result, err := runnerValue[runner.TextResult](ctx, e, runner.MethodText, &runner.PathParams{Path: e.resolve(p)})
	return result.Text, err
}
func (e *runnerEnv) FileInfo(ctx context.Context, p string) (env.FileInfo, error) {
	return runnerValue[env.FileInfo](ctx, e, runner.MethodStat, &runner.PathParams{Path: e.resolve(p)})
}
func (e *runnerEnv) CanonicalPath(ctx context.Context, p string) (string, error) {
	result, err := runnerValue[runner.PathResult](ctx, e, runner.MethodCanonical, &runner.PathParams{Path: e.resolve(p)})
	return result.Path, err
}
func (e *runnerEnv) ListDir(ctx context.Context, p string) ([]env.FileInfo, error) {
	return runnerValue[[]env.FileInfo](ctx, e, runner.MethodList, &runner.PathParams{Path: e.resolve(p)})
}
func (e *runnerEnv) Exists(ctx context.Context, p string) (bool, error) {
	_, err := e.FileInfo(ctx, p)
	if failure, ok := errors.AsType[*env.FileError](err); ok && failure.Code == env.FileErrorNotFound {
		return false, nil
	}
	return err == nil, err
}
func (e *runnerEnv) CreateDir(ctx context.Context, p string, opts *env.CreateDirOptions) error {
	params := runner.MakeDirParams{Path: e.resolve(p)}
	if opts != nil {
		params.Recursive = opts.Recursive
	}
	return e.call(ctx, runner.MethodMakeDir, &params, nil)
}
func (e *runnerEnv) Remove(ctx context.Context, p string, opts *env.RemoveOptions) error {
	params := runner.RemoveParams{Path: e.resolve(p)}
	if opts != nil {
		params.Recursive, params.Force = opts.Recursive, opts.Force
	}
	return e.call(ctx, runner.MethodRemove, &params, nil)
}
func (e *runnerEnv) RenameFile(ctx context.Context, from, to string) error {
	return e.call(ctx, runner.MethodRename, &runner.RenameParams{Path: e.resolve(from), Dest: e.resolve(to)}, nil)
}
func (e *runnerEnv) TruncateFile(ctx context.Context, p string, size int64) error {
	return e.call(ctx, runner.MethodTruncate, &runner.TruncateParams{Path: e.resolve(p), Size: size}, nil)
}
func (e *runnerEnv) FlushFile(ctx context.Context, p string) error {
	return e.call(ctx, runner.MethodFlush, &runner.PathParams{Path: e.resolve(p)}, nil)
}
func (e *runnerEnv) CreateTempDir(ctx context.Context, prefix *string) (string, error) {
	result, err := runnerValue[runner.PathResult](ctx, e, runner.MethodTempDir, &runner.TempDirParams{Prefix: prefix})
	return result.Path, err
}
func (e *runnerEnv) CreateTempFile(ctx context.Context, opts *env.CreateTempFileOptions) (string, error) {
	params := runner.TempFileParams{}
	if opts != nil {
		params.Prefix, params.Suffix = &opts.Prefix, &opts.Suffix
	}
	result, err := runnerValue[runner.PathResult](ctx, e, runner.MethodTempFile, &params)
	return result.Path, err
}
func (e *runnerEnv) write(ctx context.Context, method, p string, content any) error {
	var data []byte
	switch v := content.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return env.NewFileError(env.FileErrorInvalid, "expected string or bytes", p, nil)
	}
	return e.call(ctx, method, &runner.WriteParams{Path: e.resolve(p), Data: data}, nil)
}
func (e *runnerEnv) WriteFile(ctx context.Context, p string, v any) error {
	return e.write(ctx, runner.MethodWrite, p, v)
}
func (e *runnerEnv) AppendFile(ctx context.Context, p string, v any) error {
	return e.write(ctx, runner.MethodAppend, p, v)
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
	var result runner.DataResult
	if err := e.call(ctx, runner.MethodRead, &runner.PathParams{Path: e.resolve(p), NoFollow: opts != nil && opts.NoFollow}, &result); err != nil {
		return nil, err
	}
	info.Size = int64(len(result.Data))
	return &runnerBinary{data: result.Data, info: info}, nil
}
func (e *runnerEnv) Exec(ctx context.Context, command any, opts *env.ShellExecOptions) (env.ShellExecResult, error) {
	if opts == nil {
		opts = &env.ShellExecOptions{}
	}
	switch command.(type) {
	case string, []string:
	default:
		return env.ShellExecResult{}, fmt.Errorf("invalid command type %T", command)
	}
	params := runner.ExecParams{Cwd: e.resolve(opts.Cwd), InheritEnv: opts.InheritEnv, Timeout: opts.Timeout, Env: map[string]string{}}
	maps.Copy(params.Env, e.extra)
	maps.Copy(params.Env, opts.Env)
	code, err := e.handle.client.Exec(ctx, command, params, func(text string, stream env.ShellStream) {
		if opts.OnOutput != nil {
			opts.OnOutput(ctx, text, env.ShellOutputInfo{Stream: stream})
		}
	})
	return env.ShellExecResult{ExitCode: code}, err
}
func (e *runnerEnv) Cleanup(context.Context) error { return nil }

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
