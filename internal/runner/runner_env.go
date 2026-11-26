package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"strings"
	"sync"
	"time"

	env "github.com/MichaelKinsy/PiG/durable/env"
	"github.com/recally-io/dex/internal/runner/outbound"
)

// runnerBackend opens leases on enrolled Remote hosts.
type runnerBackend struct{ runners *Runners }

func (runnerBackend) normalize(spec Spec) (string, error) {
	if spec.Secret != "" {
		return "", fmt.Errorf("%w: a remote machine has no secret", ErrInvalidConfig)
	}
	return "{}", nil
}

func (b runnerBackend) probe(_ context.Context, _ *Spec, spec Spec) (TestResult, error) {
	session := b.runners.online(spec.ID)
	if session == nil {
		return TestResult{Error: "Outbound Machine is offline"}, nil
	}
	meta := session.Info()
	return TestResult{OK: true, Platform: meta.OS, Arch: meta.Arch, Home: meta.Home}, nil
}

func (b runnerBackend) open(ctx context.Context, agentID string, spec Spec) (lease, error) {
	digest, err := b.runners.identity(ctx, spec.ID)
	if err != nil {
		return nil, err
	}
	b.runners.mu.Lock()
	session, err := b.runners.sessionLocked(spec.ID, digest)
	b.runners.mu.Unlock()
	if err != nil {
		return nil, err
	}
	info := session.Info()
	base := path.Join(path.Dir(info.Workspace), "workspaces")
	h := &runnerHandle{runners: b.runners, id: spec.ID, digest: digest, ready: info, workspace: remoteBotWorkspace(base, "/", agentID)}
	if err := createRemoteBotWorkspace(ctx, h); err != nil {
		return nil, fmt.Errorf("create runner workspace: %w", err)
	}
	return h, nil
}

func (b runnerBackend) changed(ctx context.Context, old Spec, cur *Spec) error {
	if cur != nil {
		return nil
	}
	return b.runners.Revoke(ctx, old.ID)
}

// release keeps the machine workspace: the machine belongs to the user.
func (runnerBackend) release(context.Context, string, Spec, string) error { return nil }
func (runnerBackend) disposable() bool                                    { return false }
func (b runnerBackend) identity(ctx context.Context, envID string) (string, error) {
	return b.runners.identity(ctx, envID)
}

type enrollmentKey struct{}

// RequireEnrollment pins Runner operations to the enrollment recorded by the caller.
func RequireEnrollment(ctx context.Context, digest string) context.Context {
	return context.WithValue(ctx, enrollmentKey{}, digest)
}

// EnrolledRunnerID returns the Outbound Runner ID of the opened lease.
func EnrolledRunnerID(h Handle) string {
	if machine := outboundHandle(h); machine != nil {
		return machine.id
	}
	return ""
}

func outboundHandle(h Handle) *runnerHandle {
	wrapped, _ := h.(stampedHandle)
	machine, _ := wrapped.lease.(*runnerHandle)
	return machine
}

// CheckEnrollment rejects a handle that does not match the pinned enrollment.
func CheckEnrollment(ctx context.Context, h Handle) error {
	expected, _ := ctx.Value(enrollmentKey{}).(string)
	if expected == "" {
		return nil
	}
	if machine := outboundHandle(h); machine == nil || machine.digest != expected {
		return fmt.Errorf("%w: the Runner enrollment changed", ErrUnavailable)
	}
	return nil
}

// runnerHandle is a lease on the enrollment of a machine. It finds the live session at each
// call, so a reconnect does not make the lease stale.
type runnerHandle struct {
	runners *Runners
	id      string
	digest  string
	// ready is the report of the session that opened the lease. Paths of the lease depend on it.
	ready     outbound.Ready
	workspace string
}

func (h *runnerHandle) Kind() Kind        { return KindRunner }
func (h *runnerHandle) Workspace() string { return h.workspace }
func (h *runnerHandle) Home() string      { return h.ready.Home }

// SkillConnection identifies a runner connection without invalidating its lease.
// Other environments replace the lease when their file namespace changes.
func SkillConnection(ctx context.Context, h Handle) (*outbound.Client, error) {
	wrapped, ok := h.(stampedHandle)
	if !ok {
		return nil, nil
	}
	machine, ok := wrapped.lease.(*runnerHandle)
	if !ok {
		return nil, nil
	}
	return machine.runners.wait(ctx, machine.id, machine.digest)
}

// do runs fn on the live session. It waits for a reconnect before the call starts. A session
// that closes before the call is dispatched reports outbound.ErrOffline, so one more attempt on
// the next session cannot repeat work. A lost dispatched call reports outbound.ErrUnknown, and
// do never repeats it.
func (h *runnerHandle) do(ctx context.Context, method string, fn func(context.Context, *outbound.Client) error) error {
	callCtx, waitDone := outbound.TraceConnectionWait(ctx, h.id, method)
	started := time.Now()
	for attempt := 0; ; attempt++ {
		waitStart := time.Now()
		client, err := h.runners.wait(callCtx, h.id, h.digest)
		waitDone(waitStart, err)
		if err != nil {
			return err
		}
		if info := client.Info(); info.Workspace != h.ready.Workspace || info.Home != h.ready.Home {
			// The machine runs from another state directory now, so the paths of the lease are wrong.
			outbound.TraceWorkspaceMismatch(callCtx, started)
			return fmt.Errorf("%w: the machine workspace changed; reload the bot", ErrUnavailable)
		}
		err = fn(callCtx, client)
		if !errors.Is(err, outbound.ErrOffline) {
			return err
		}
		if attempt > 0 {
			return errRunnerOffline()
		}
	}
}

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
	home := e.handle.Home()
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
func (e *runnerEnv) call(ctx context.Context, method string, params outbound.Tagger, result any) error {
	return e.handle.do(ctx, method, func(ctx context.Context, client *outbound.Client) error {
		return client.Call(ctx, method, params, result)
	})
}
func runnerValue[T any, P outbound.Tagger](ctx context.Context, e *runnerEnv, method string, params P) (T, error) {
	var value T
	err := e.call(ctx, method, params, &value)
	return value, err
}
func (e *runnerEnv) ReadBinaryFile(ctx context.Context, p string) ([]byte, error) {
	var result outbound.DataResult
	err := e.call(ctx, outbound.MethodRead, &outbound.PathParams{Path: e.resolve(p)}, &result)
	return result.Data, err
}
func (e *runnerEnv) ReadTextFile(ctx context.Context, p string) (string, error) {
	result, err := runnerValue[outbound.TextResult](ctx, e, outbound.MethodText, &outbound.PathParams{Path: e.resolve(p)})
	return result.Text, err
}
func (e *runnerEnv) FileInfo(ctx context.Context, p string) (env.FileInfo, error) {
	return runnerValue[env.FileInfo](ctx, e, outbound.MethodStat, &outbound.PathParams{Path: e.resolve(p)})
}
func (e *runnerEnv) CanonicalPath(ctx context.Context, p string) (string, error) {
	result, err := runnerValue[outbound.PathResult](ctx, e, outbound.MethodCanonical, &outbound.PathParams{Path: e.resolve(p)})
	return result.Path, err
}
func (e *runnerEnv) ListDir(ctx context.Context, p string) ([]env.FileInfo, error) {
	return runnerValue[[]env.FileInfo](ctx, e, outbound.MethodList, &outbound.PathParams{Path: e.resolve(p)})
}
func (e *runnerEnv) Exists(ctx context.Context, p string) (bool, error) {
	_, err := e.FileInfo(ctx, p)
	if failure, ok := errors.AsType[*env.FileError](err); ok && failure.Code == env.FileErrorNotFound {
		return false, nil
	}
	return err == nil, err
}
func (e *runnerEnv) CreateDir(ctx context.Context, p string, opts *env.CreateDirOptions) error {
	params := outbound.MakeDirParams{Path: e.resolve(p)}
	if opts != nil {
		params.Recursive = opts.Recursive
	}
	return e.call(ctx, outbound.MethodMakeDir, &params, nil)
}
func (e *runnerEnv) Remove(ctx context.Context, p string, opts *env.RemoveOptions) error {
	params := outbound.RemoveParams{Path: e.resolve(p)}
	if opts != nil {
		params.Recursive, params.Force = opts.Recursive, opts.Force
	}
	return e.call(ctx, outbound.MethodRemove, &params, nil)
}
func (e *runnerEnv) RenameFile(ctx context.Context, from, to string) error {
	return e.call(ctx, outbound.MethodRename, &outbound.RenameParams{Path: e.resolve(from), Dest: e.resolve(to)}, nil)
}
func (e *runnerEnv) TruncateFile(ctx context.Context, p string, size int64) error {
	return e.call(ctx, outbound.MethodTruncate, &outbound.TruncateParams{Path: e.resolve(p), Size: size}, nil)
}
func (e *runnerEnv) FlushFile(ctx context.Context, p string) error {
	return e.call(ctx, outbound.MethodFlush, &outbound.PathParams{Path: e.resolve(p)}, nil)
}
func (e *runnerEnv) CreateTempDir(ctx context.Context, prefix *string) (string, error) {
	result, err := runnerValue[outbound.PathResult](ctx, e, outbound.MethodTempDir, &outbound.TempDirParams{Prefix: prefix})
	return result.Path, err
}
func (e *runnerEnv) CreateTempFile(ctx context.Context, opts *env.CreateTempFileOptions) (string, error) {
	params := outbound.TempFileParams{}
	if opts != nil {
		params.Prefix, params.Suffix = &opts.Prefix, &opts.Suffix
	}
	result, err := runnerValue[outbound.PathResult](ctx, e, outbound.MethodTempFile, &params)
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
	return e.call(ctx, method, &outbound.WriteParams{Path: e.resolve(p), Data: data}, nil)
}
func (e *runnerEnv) WriteFile(ctx context.Context, p string, v any) error {
	return e.write(ctx, outbound.MethodWrite, p, v)
}
func (e *runnerEnv) AppendFile(ctx context.Context, p string, v any) error {
	return e.write(ctx, outbound.MethodAppend, p, v)
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
	var result outbound.DataResult
	if err := e.call(ctx, outbound.MethodRead, &outbound.PathParams{Path: e.resolve(p), NoFollow: opts != nil && opts.NoFollow}, &result); err != nil {
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
	params := outbound.ExecParams{Cwd: e.resolve(opts.Cwd), InheritEnv: opts.InheritEnv, Timeout: opts.Timeout, Env: map[string]string{}}
	maps.Copy(params.Env, e.extra)
	maps.Copy(params.Env, opts.Env)
	var code int
	err := e.handle.do(ctx, outbound.MethodExec, func(ctx context.Context, client *outbound.Client) error {
		var err error
		code, err = client.Exec(ctx, command, params, func(text string, stream env.ShellStream) {
			if opts.OnOutput != nil {
				opts.OnOutput(ctx, text, env.ShellOutputInfo{Stream: stream})
			}
		})
		return err
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
