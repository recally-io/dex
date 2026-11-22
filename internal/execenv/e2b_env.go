package execenv

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"uuid"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
)

const (
	e2bReadChunkBytes = 512 * 1024
	e2bScanChunkBytes = 64 * 1024
	e2bSafeIntMax     = 1<<53 - 1
)

type e2bExecEnv struct {
	id         string
	home       string
	timeoutSec int
	shellEnv   map[string]string
	handle     *e2bHandle

	cwdMu sync.RWMutex
	cwd   string
}

func (e *e2bExecEnv) acquire(ctx context.Context) (*envdClient, string, error) {
	if s := e.handle.override; s != nil {
		return s.envd, s.sandboxID, ctx.Err()
	}
	session, err := e.handle.rt.acquire(ctx, e.handle.agentID, e.handle.spec)
	return session.envd, session.sandboxID, err
}

func (e *e2bExecEnv) renew(ctx context.Context, sbID string) {
	if cfg, parseErr := parseE2BConfig(e.handle.spec.Config); parseErr == nil {
		_ = e.handle.rt.control(cfg, e.handle.spec.Secret).setTimeout(ctx, sbID, e.handle.timeoutSec)
	}
}

var _ durableenv.ExecutionEnv = (*e2bExecEnv)(nil)

func (e *e2bExecEnv) Id() string { return e.id } //nolint:revive // implements durableenv.FileSystem.Id

func (e *e2bExecEnv) Cwd() string {
	e.cwdMu.RLock()
	defer e.cwdMu.RUnlock()
	return e.cwd
}

func (e *e2bExecEnv) SetCwd(cwd string) {
	e.cwdMu.Lock()
	e.cwd = resolveE2BTarget(e.home, e.cwd, cwd)
	e.cwdMu.Unlock()
}

func (e *e2bExecEnv) resolve(raw string) string {
	return resolveE2BTarget(e.home, e.Cwd(), raw)
}

func resolveE2BTarget(home, baseCwd, raw string) string {
	if raw == "" {
		return path.Clean(cmp.Or(baseCwd, "/home/user"))
	}
	if raw == "~" {
		return path.Clean(cmp.Or(home, "/home/user"))
	}
	if rest, ok := strings.CutPrefix(raw, "~/"); ok {
		return path.Join(cmp.Or(home, "/home/user"), rest)
	}
	if strings.HasPrefix(raw, "/") {
		return path.Clean(raw)
	}
	return path.Join(cmp.Or(baseCwd, "/home/user"), raw)
}

func (e *e2bExecEnv) AbsolutePath(ctx context.Context, targetPath string) (string, error) {
	if err := checkE2BAborted(ctx, targetPath); err != nil {
		return "", err
	}
	return e.resolve(targetPath), nil
}

func (e *e2bExecEnv) JoinPath(ctx context.Context, parts []string) (string, error) {
	if err := checkE2BAborted(ctx, ""); err != nil {
		return "", err
	}
	if len(parts) == 0 {
		return ".", nil
	}
	return path.Join(parts...), nil
}

func (e *e2bExecEnv) CanonicalPath(ctx context.Context, targetPath string) (string, error) {
	resolved := e.resolve(targetPath)
	if err := checkE2BAborted(ctx, resolved); err != nil {
		return "", err
	}
	envd, _, err := e.acquire(ctx)
	if err != nil {
		return "", durableenv.NewFileError(durableenv.FileErrorUnknown, err.Error(), resolved, err)
	}
	entry, err := envd.stat(ctx, resolved)
	if err != nil {
		return "", mapE2BFileError(err, resolved)
	}
	if entry.SymlinkTarget != nil && *entry.SymlinkTarget != "" {
		target := *entry.SymlinkTarget
		if !strings.HasPrefix(target, "/") {
			target = path.Join(path.Dir(resolved), target)
		}
		if _, err := envd.stat(ctx, target); err != nil {
			return "", mapE2BFileError(err, resolved)
		}
		return path.Clean(target), nil
	}
	if entry.Path != "" && strings.HasPrefix(entry.Path, "/") {
		return path.Clean(entry.Path), nil
	}
	return resolved, nil
}

func (e *e2bExecEnv) Exists(ctx context.Context, targetPath string) (bool, error) {
	_, err := e.FileInfo(ctx, targetPath)
	if err == nil {
		return true, nil
	}
	if fe, ok := errors.AsType[*durableenv.FileError](err); ok && fe.Code == durableenv.FileErrorNotFound {
		return false, nil
	}
	return false, err
}

func (e *e2bExecEnv) FileInfo(ctx context.Context, targetPath string) (durableenv.FileInfo, error) {
	resolved := e.resolve(targetPath)
	if err := checkE2BAborted(ctx, resolved); err != nil {
		return durableenv.FileInfo{}, err
	}
	envd, _, err := e.acquire(ctx)
	if err != nil {
		return durableenv.FileInfo{}, durableenv.NewFileError(durableenv.FileErrorUnknown, err.Error(), resolved, err)
	}
	entry, err := envd.stat(ctx, resolved)
	if err != nil {
		return durableenv.FileInfo{}, mapE2BFileError(err, resolved)
	}
	return toE2BFileInfo(resolved, *entry, true), nil
}

func (e *e2bExecEnv) ListDir(ctx context.Context, targetPath string) ([]durableenv.FileInfo, error) {
	resolved := e.resolve(targetPath)
	if err := checkE2BAborted(ctx, resolved); err != nil {
		return nil, err
	}
	envd, _, err := e.acquire(ctx)
	if err != nil {
		return nil, durableenv.NewFileError(durableenv.FileErrorUnknown, err.Error(), resolved, err)
	}
	entries, err := envd.listDir(ctx, resolved)
	if err != nil {
		return nil, mapE2BFileError(err, resolved)
	}
	out := make([]durableenv.FileInfo, 0, len(entries))
	for _, entry := range entries {
		entryPath := entry.Path
		if entryPath == "" {
			entryPath = path.Join(resolved, entry.Name)
		}
		if path.Clean(entryPath) == resolved {
			continue
		}
		out = append(out, toE2BFileInfo(entryPath, entry, true))
	}
	return out, nil
}

func (e *e2bExecEnv) OpenDirReader(ctx context.Context, targetPath string) (durableenv.DirReader, error) {
	entries, err := e.ListDir(ctx, targetPath)
	if err != nil {
		return nil, err
	}
	return &sliceDirReader{entries: entries}, nil
}

func (e *e2bExecEnv) ReadBinaryFile(ctx context.Context, targetPath string) ([]byte, error) {
	resolved := e.resolve(targetPath)
	if err := checkE2BAborted(ctx, resolved); err != nil {
		return nil, err
	}
	envd, _, err := e.acquire(ctx)
	if err != nil {
		return nil, durableenv.NewFileError(durableenv.FileErrorUnknown, err.Error(), resolved, err)
	}
	data, err := envd.downloadFile(ctx, resolved)
	if err != nil {
		return nil, mapE2BFileError(err, resolved)
	}
	return data, checkE2BAborted(ctx, resolved)
}

func (e *e2bExecEnv) ReadTextFile(ctx context.Context, targetPath string) (string, error) {
	data, err := e.ReadBinaryFile(ctx, targetPath)
	if err != nil {
		return "", err
	}
	return strings.ToValidUTF8(string(data), "\uFFFD"), nil
}

func (e *e2bExecEnv) OpenBinaryReader(ctx context.Context, targetPath string, options *durableenv.OpenBinaryReaderOptions) (durableenv.BinaryReader, error) {
	resolved := e.resolve(targetPath)
	if err := checkE2BAborted(ctx, resolved); err != nil {
		return nil, err
	}
	envd, _, err := e.acquire(ctx)
	if err != nil {
		return nil, durableenv.NewFileError(durableenv.FileErrorUnknown, err.Error(), resolved, err)
	}
	entry, err := envd.stat(ctx, resolved)
	if err != nil {
		return nil, mapE2BFileError(err, resolved)
	}
	isLink := entry.SymlinkTarget != nil || entry.Type == "FILE_TYPE_SYMLINK"
	if options != nil && options.NoFollow && isLink {
		return nil, durableenv.NewFileError(durableenv.FileErrorInvalid, "Refusing to follow a symbolic link", resolved, nil)
	}
	if entry.Type == "FILE_TYPE_DIRECTORY" {
		return nil, durableenv.NewFileError(durableenv.FileErrorIsDirectory, "EISDIR: illegal operation on a directory, read", resolved, nil)
	}
	if isLink && entry.SymlinkTarget != nil && *entry.SymlinkTarget != "" {
		target := *entry.SymlinkTarget
		if !strings.HasPrefix(target, "/") {
			target = path.Join(path.Dir(resolved), target)
		}
		targetEntry, statErr := envd.stat(ctx, target)
		if statErr != nil {
			return nil, mapE2BFileError(statErr, resolved)
		}
		if targetEntry.Type == "FILE_TYPE_DIRECTORY" {
			return nil, durableenv.NewFileError(durableenv.FileErrorIsDirectory, "EISDIR: illegal operation on a directory, read", resolved, nil)
		}
		entry = targetEntry
	}
	if entry.Type != "FILE_TYPE_FILE" {
		return nil, durableenv.NewFileError(durableenv.FileErrorInvalid, "Not a regular file", resolved, nil)
	}
	return &e2bBinaryReader{
		env:  e,
		path: resolved,
		info: toE2BFileInfo(resolved, *entry, false),
	}, nil
}

func (e *e2bExecEnv) WriteFile(ctx context.Context, targetPath string, content any) error {
	resolved := e.resolve(targetPath)
	if err := checkE2BAborted(ctx, resolved); err != nil {
		return err
	}
	var data []byte
	switch typed := content.(type) {
	case string:
		data = []byte(typed)
	case []byte:
		data = typed
	default:
		return durableenv.NewFileError(durableenv.FileErrorInvalid, fmt.Sprintf("unsupported content %T", content), resolved, nil)
	}
	envd, _, err := e.acquire(ctx)
	if err != nil {
		return durableenv.NewFileError(durableenv.FileErrorUnknown, err.Error(), resolved, err)
	}
	if parent := path.Dir(resolved); parent != "/" && parent != "." {
		if err := makeEnvdDirAll(ctx, envd, parent); err != nil {
			return mapE2BFileError(err, resolved)
		}
	}
	if err := envd.uploadFile(ctx, resolved, data); err != nil {
		return mapE2BFileError(err, resolved)
	}
	return checkE2BAborted(ctx, resolved)
}

func (e *e2bExecEnv) CreateDir(ctx context.Context, targetPath string, options *durableenv.CreateDirOptions) error {
	resolved := e.resolve(targetPath)
	if err := checkE2BAborted(ctx, resolved); err != nil {
		return err
	}
	envd, _, err := e.acquire(ctx)
	if err != nil {
		return durableenv.NewFileError(durableenv.FileErrorUnknown, err.Error(), resolved, err)
	}
	if options != nil && options.Recursive != nil && *options.Recursive {
		if err := makeEnvdDirAll(ctx, envd, resolved); err != nil {
			return mapE2BFileError(err, resolved)
		}
		return nil
	}
	if err := envd.makeDir(ctx, resolved); err != nil {
		return mapE2BFileError(err, resolved)
	}
	return nil
}

func (e *e2bExecEnv) Remove(ctx context.Context, targetPath string, options *durableenv.RemoveOptions) error {
	resolved := e.resolve(targetPath)
	if err := checkE2BAborted(ctx, resolved); err != nil {
		return err
	}
	envd, _, err := e.acquire(ctx)
	if err != nil {
		return durableenv.NewFileError(durableenv.FileErrorUnknown, err.Error(), resolved, err)
	}
	if err := envd.remove(ctx, resolved); err != nil {
		fe := mapE2BFileError(err, resolved)
		if options != nil && options.Force && fe.Code == durableenv.FileErrorNotFound {
			return nil
		}
		return fe
	}
	return nil
}

// Optional or jsonl-only FileSystem methods return FileErrorNotSupported.
func (e *e2bExecEnv) ListFiles(_ context.Context, p string, _ *durableenv.ListFilesOptions) (durableenv.FileListing, error) {
	return durableenv.FileListing{}, notSupportedE2B(e.resolve(p))
}
func (e *e2bExecEnv) Watch(_ context.Context, _ []durableenv.WatchTarget, _ func(durableenv.FileChange)) (durableenv.FileWatcher, error) {
	return nil, notSupportedE2B(e.Cwd())
}
func (e *e2bExecEnv) OpenTextLineReader(_ context.Context, p string) (durableenv.TextLineReader, error) {
	return nil, notSupportedE2B(e.resolve(p))
}
func (e *e2bExecEnv) ReadTextLines(_ context.Context, p string, _ *durableenv.ReadTextLinesOptions) ([]string, error) {
	return nil, notSupportedE2B(e.resolve(p))
}
func (e *e2bExecEnv) AppendFile(_ context.Context, p string, _ any) error {
	return notSupportedE2B(e.resolve(p))
}
func (e *e2bExecEnv) TruncateFile(_ context.Context, p string, _ int64) error {
	return notSupportedE2B(e.resolve(p))
}
func (e *e2bExecEnv) FlushFile(_ context.Context, p string) error {
	return notSupportedE2B(e.resolve(p))
}
func (e *e2bExecEnv) RenameFile(_ context.Context, p, _ string) error {
	return notSupportedE2B(e.resolve(p))
}
func (e *e2bExecEnv) CreateTempDir(_ context.Context, _ *string) (string, error) {
	return "", notSupportedE2B(e.Cwd())
}
func (e *e2bExecEnv) CreateTempFile(_ context.Context, _ *durableenv.CreateTempFileOptions) (string, error) {
	return "", notSupportedE2B(e.Cwd())
}

func (e *e2bExecEnv) Cleanup(context.Context) error { return nil }

func (e *e2bExecEnv) Exec(ctx context.Context, command any, options *durableenv.ShellExecOptions) (durableenv.ShellExecResult, error) {
	if options == nil {
		options = &durableenv.ShellExecOptions{}
	}
	if ctx.Err() != nil {
		return durableenv.ShellExecResult{}, durableenv.NewExecutionError(durableenv.ExecutionErrorAborted, "Operation aborted", ctx.Err())
	}
	var timeout time.Duration
	if options.Timeout != nil {
		if math.IsNaN(*options.Timeout) || math.IsInf(*options.Timeout, 0) || *options.Timeout <= 0 {
			return durableenv.ShellExecResult{}, durableenv.NewExecutionError(durableenv.ExecutionErrorSpawnError, "Invalid timeout", nil)
		}
		timeout = time.Duration(*options.Timeout * float64(time.Second))
	}

	cwd := e.Cwd()
	if options.Cwd != "" {
		cwd = e.resolve(options.Cwd)
	}
	procCfg, err := e.buildProcessConfig(command, cwd, options)
	if err != nil {
		return durableenv.ShellExecResult{}, err
	}

	envd, sbID, err := e.acquire(ctx)
	if err != nil {
		return durableenv.ShellExecResult{}, durableenv.NewExecutionError(durableenv.ExecutionErrorSpawnError, err.Error(), err)
	}

	streamCtx, cancelStream := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelStream()

	var (
		mu       sync.Mutex
		pid      uint32
		hasPID   bool
		timedOut bool
	)

	killRemote := func() {
		mu.Lock()
		p, ok := pid, hasPID
		mu.Unlock()
		if ok {
			sigCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = envd.sendSignal(sigCtx, p, "SIGNAL_SIGKILL")
		}
	}

	stopAbort := context.AfterFunc(ctx, func() {
		killRemote()
		cancelStream()
	})
	defer stopAbort()

	if timeout > 0 {
		timer := time.AfterFunc(timeout, func() {
			mu.Lock()
			timedOut = true
			mu.Unlock()
			killRemote()
			cancelStream()
		})
		defer timer.Stop()
	}

	// Keep sandbox TTL alive during long-running commands; E2B autoPause handles idle periods.
	if e.handle != nil && sbID != "" && e.timeoutSec > 0 {
		interval := max(time.Duration(e.timeoutSec)*time.Second/2, 5*time.Second)
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-streamCtx.Done():
					return
				case <-ticker.C:
					e.renew(streamCtx, sbID)
				}
			}
		}()
	}

	stdoutDec := durableenv.NewStreamDecoder()
	stderrDec := durableenv.NewStreamDecoder()
	var (
		spillBuf     bytes.Buffer
		seenLines    int
		spilled      bool
		spillPath    string
		exitCode     int
		gotEnd       bool
		processError string
	)

	feed := func(raw []byte, stream durableenv.ShellStream, dec *durableenv.StreamDecoder) {
		if len(raw) == 0 {
			return
		}
		if options.Spill != nil {
			spillBuf.Write(raw)
			seenLines += bytes.Count(raw, []byte{'\n'})
			if !spilled && ((options.Spill.AfterBytes > 0 && int64(spillBuf.Len()) > options.Spill.AfterBytes) || (options.Spill.AfterLines > 0 && int64(seenLines) > options.Spill.AfterLines)) {
				spilled = true
				spillPath = "/tmp/pi-output-" + uuid.NewV7().String() + ".log"
			}
		}
		text := dec.Decode(raw)
		if text != "" && options.OnOutput != nil {
			options.OnOutput(ctx, text, durableenv.ShellOutputInfo{Stream: stream})
		}
	}

	rpcErr := envd.startProcess(streamCtx, procCfg, func(ev envdProcessEvent) error {
		switch {
		case ev.Start != nil:
			mu.Lock()
			pid, hasPID = ev.Start.PID, true
			mu.Unlock()
		case ev.Data != nil:
			feed(ev.Data.Stdout, durableenv.ShellStdout, stdoutDec)
			feed(ev.Data.Stderr, durableenv.ShellStderr, stderrDec)
		case ev.End != nil:
			gotEnd = true
			exitCode = ev.End.ExitCode
			if ev.End.Error != nil {
				processError = *ev.End.Error
			}
		}
		return nil
	})

	for _, tail := range []struct {
		text   string
		stream durableenv.ShellStream
	}{
		{stdoutDec.End(), durableenv.ShellStdout},
		{stderrDec.End(), durableenv.ShellStderr},
	} {
		if tail.text != "" && options.OnOutput != nil {
			options.OnOutput(ctx, tail.text, durableenv.ShellOutputInfo{Stream: tail.stream})
		}
	}

	if spilled && spillPath != "" {
		upCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if err := envd.uploadFile(upCtx, spillPath, spillBuf.Bytes()); err != nil {
			spillPath = ""
		}
		cancel()
	}

	mu.Lock()
	didTimeout := timedOut
	mu.Unlock()

	if didTimeout {
		ee := durableenv.NewExecutionError(durableenv.ExecutionErrorTimeout, fmt.Sprintf("timeout:%g", *options.Timeout), nil)
		ee.SpillPath = spillPath
		return durableenv.ShellExecResult{}, ee
	}
	if ctx.Err() != nil {
		ee := durableenv.NewExecutionError(durableenv.ExecutionErrorAborted, "Operation aborted", ctx.Err())
		ee.SpillPath = spillPath
		return durableenv.ShellExecResult{}, ee
	}
	if rpcErr != nil {
		ee := durableenv.NewExecutionError(durableenv.ExecutionErrorSpawnError, rpcErr.Error(), rpcErr)
		ee.SpillPath = spillPath
		return durableenv.ShellExecResult{}, ee
	}
	if !gotEnd && processError != "" {
		return durableenv.ShellExecResult{}, durableenv.NewExecutionError(durableenv.ExecutionErrorSpawnError, processError, nil)
	}
	return durableenv.ShellExecResult{ExitCode: exitCode, SpillPath: spillPath}, nil
}

func (e *e2bExecEnv) buildProcessConfig(command any, cwd string, options *durableenv.ShellExecOptions) (map[string]any, error) {
	var prog string
	var args []string
	switch typed := command.(type) {
	case string:
		prog = "/bin/bash"
		args = []string{"-c", typed}
	case []string:
		if len(typed) == 0 {
			return nil, durableenv.NewExecutionError(durableenv.ExecutionErrorSpawnError, "Empty argv", nil)
		}
		prog = typed[0]
		args = slices.Clone(typed[1:])
	default:
		return nil, durableenv.NewExecutionError(durableenv.ExecutionErrorSpawnError, fmt.Sprintf("unsupported command %T", command), nil)
	}

	inheritEnv := options.InheritEnv == nil || *options.InheritEnv
	if inheritEnv {
		merged := maps.Clone(e.shellEnv)
		if merged == nil {
			merged = map[string]string{}
		}
		maps.Copy(merged, options.Env)
		return map[string]any{"cmd": prog, "args": args, "envs": merged, "cwd": cwd}, nil
	}

	envArgs := []string{"-i"}
	for _, k := range slices.Sorted(maps.Keys(options.Env)) {
		envArgs = append(envArgs, k+"="+options.Env[k])
	}
	envArgs = append(envArgs, prog)
	envArgs = append(envArgs, args...)
	return map[string]any{"cmd": "/usr/bin/env", "args": envArgs, "cwd": cwd}, nil
}

type e2bBinaryReader struct {
	env    *e2bExecEnv
	path   string
	info   durableenv.FileInfo
	mu     sync.RWMutex
	closed bool
}

func (r *e2bBinaryReader) Info(ctx context.Context) (durableenv.FileInfo, error) {
	if err := checkE2BAborted(ctx, r.path); err != nil {
		return durableenv.FileInfo{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return durableenv.FileInfo{}, durableenv.NewFileError(durableenv.FileErrorInvalid, "Binary reader is closed", r.path, nil)
	}
	return r.info, nil
}

func (r *e2bBinaryReader) Read(ctx context.Context, offset, length int64) ([]byte, error) {
	if err := checkE2BAborted(ctx, r.path); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return nil, durableenv.NewFileError(durableenv.FileErrorInvalid, "Binary reader is closed", r.path, nil)
	}
	if offset < 0 || offset > e2bSafeIntMax || length < 0 || length > e2bSafeIntMax {
		return nil, durableenv.NewFileError(durableenv.FileErrorInvalid, "Invalid range", r.path, nil)
	}
	if length == 0 {
		return []byte{}, nil
	}
	envd, _, err := r.env.acquire(ctx)
	if err != nil {
		return nil, durableenv.NewFileError(durableenv.FileErrorUnknown, err.Error(), r.path, err)
	}
	var out []byte
	for total := int64(0); total < length; {
		want := min(length-total, e2bReadChunkBytes)
		chunk, err := envd.downloadRange(ctx, r.path, offset+total, want)
		if err != nil {
			return nil, mapE2BFileError(err, r.path)
		}
		if len(chunk) == 0 {
			break
		}
		out = append(out, chunk...)
		total += int64(len(chunk))
		if int64(len(chunk)) < want {
			break
		}
	}
	if out == nil {
		out = []byte{}
	}
	return out, checkE2BAborted(ctx, r.path)
}

func (r *e2bBinaryReader) ScanLines(ctx context.Context, options durableenv.ScanLinesOptions) (durableenv.LineScan, error) {
	if err := checkE2BAborted(ctx, r.path); err != nil {
		return durableenv.LineScan{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return durableenv.LineScan{}, durableenv.NewFileError(durableenv.FileErrorInvalid, "Binary reader is closed", r.path, nil)
	}
	scanner, err := durableenv.NewLineScanner(options.StartLine, options.EndLine)
	if err != nil {
		return durableenv.LineScan{}, durableenv.NewFileError(durableenv.FileErrorInvalid, "Invalid line range", r.path, nil)
	}
	envd, _, err := r.env.acquire(ctx)
	if err != nil {
		return durableenv.LineScan{}, durableenv.NewFileError(durableenv.FileErrorUnknown, err.Error(), r.path, err)
	}
	for pos := int64(0); ; {
		chunk, err := envd.downloadRange(ctx, r.path, pos, e2bScanChunkBytes)
		if err != nil {
			return durableenv.LineScan{}, mapE2BFileError(err, r.path)
		}
		if err := checkE2BAborted(ctx, r.path); err != nil {
			return durableenv.LineScan{}, err
		}
		if len(chunk) == 0 {
			return scanner.Finish(), nil
		}
		scanner.Push(chunk)
		pos += int64(len(chunk))
		if int64(len(chunk)) < e2bScanChunkBytes {
			return scanner.Finish(), nil
		}
	}
}

func (r *e2bBinaryReader) Close(context.Context) error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return nil
}

type sliceDirReader struct {
	entries []durableenv.FileInfo
	offset  int
}

func (r *sliceDirReader) Next(_ context.Context, maxEntries int) (durableenv.DirPage, error) {
	if r.offset >= len(r.entries) {
		return durableenv.DirPage{Entries: []durableenv.FileInfo{}, Done: true}, nil
	}
	limit := max(maxEntries, 1)
	end := min(r.offset+limit, len(r.entries))
	page := r.entries[r.offset:end]
	r.offset = end
	return durableenv.DirPage{Entries: page, Done: r.offset >= len(r.entries)}, nil
}

func (r *sliceDirReader) Close(context.Context) error { return nil }

func toE2BFileInfo(resolved string, entry envdEntry, keepSymlink bool) durableenv.FileInfo {
	kind := durableenv.FileKindFile
	switch {
	case keepSymlink && (entry.SymlinkTarget != nil || entry.Type == "FILE_TYPE_SYMLINK"):
		kind = durableenv.FileKindSymlink
	case entry.Type == "FILE_TYPE_DIRECTORY":
		kind = durableenv.FileKindDirectory
	}
	var mtime float64
	if entry.ModifiedTime != nil {
		mtime = float64(entry.ModifiedTime.UnixMilli())
	}
	name := cmp.Or(entry.Name, path.Base(resolved))
	if !utf8.ValidString(name) {
		name = strings.ToValidUTF8(name, "\uFFFD")
	}
	return durableenv.FileInfo{
		Name:    name,
		Path:    resolved,
		Kind:    kind,
		Size:    int64(entry.Size),
		MtimeMs: mtime,
	}
}

func checkE2BAborted(ctx context.Context, targetPath string) error {
	if ctx.Err() != nil {
		return durableenv.NewFileError(durableenv.FileErrorAborted, "Operation aborted", targetPath, ctx.Err())
	}
	return nil
}

func notSupportedE2B(targetPath string) error {
	return durableenv.NewFileError(durableenv.FileErrorNotSupported, "Operation not supported in E2B environment", targetPath, nil)
}

func mapE2BFileError(err error, targetPath string) *durableenv.FileError {
	if fe, ok := errors.AsType[*durableenv.FileError](err); ok {
		return fe
	}
	code := durableenv.FileErrorUnknown
	msg := err.Error()
	if ce, ok := errors.AsType[*envdConnectError](err); ok {
		switch ce.Code {
		case "not_found":
			code = durableenv.FileErrorNotFound
		case "permission_denied", "unauthenticated":
			code = durableenv.FileErrorPermissionDenied
		case "already_exists":
			code = durableenv.FileErrorInvalid
		case "invalid_argument", "failed_precondition":
			if strings.Contains(strings.ToLower(ce.Message), "directory") {
				code = durableenv.FileErrorIsDirectory
			} else {
				code = durableenv.FileErrorInvalid
			}
		}
	}
	return durableenv.NewFileError(code, msg, targetPath, err)
}
