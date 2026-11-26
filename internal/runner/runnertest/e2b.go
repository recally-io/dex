package runnertest

import (
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// FakeE2BCounts tracks control-plane operations observed by FakeE2BServer.
type FakeE2BCounts struct {
	Created   int
	Connected int
	Renewed   int
	Killed    int
}

type fakeSandbox struct {
	id     string
	token  string
	root   string
	paused bool
	killed bool
}

// FakeE2BServer simulates E2B's control-plane REST API and sandbox envd endpoints.
type FakeE2BServer struct {
	apiKey  string
	apiMux  *http.ServeMux
	envdMux *http.ServeMux
	apiSrv  *httptest.Server
	envdSrv *httptest.Server

	mu                 sync.Mutex
	nextID             int
	counts             FakeE2BCounts
	lastAutoPause      bool
	lastAutoResume     bool
	lastRunnerID       string
	connectUnsupported bool
	sandboxes          map[string]*fakeSandbox
	procs              map[uint32]*exec.Cmd
}

// NewFakeE2B starts an isolated FakeE2BServer expecting apiKey.
func NewFakeE2B(t *testing.T, apiKey string) *FakeE2BServer {
	t.Helper()
	s := &FakeE2BServer{
		apiKey:    apiKey,
		sandboxes: map[string]*fakeSandbox{},
		procs:     map[uint32]*exec.Cmd{},
	}

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("POST /v2/sandboxes", s.withAPIKey(func(w http.ResponseWriter, r *http.Request) {
		var createReq struct {
			AutoPause  bool              `json:"autoPause"`
			Metadata   map[string]string `json:"metadata"`
			AutoResume *struct {
				Enabled bool `json:"enabled"`
			} `json:"autoResume"`
		}
		if r.Body != nil {
			_ = json.UnmarshalRead(r.Body, &createReq)
		}
		s.mu.Lock()
		s.nextID++
		id := fmt.Sprintf("sbx-%d", s.nextID)
		tok := "tok-" + id
		root := t.TempDir()
		allowSkillMirrorCleanup(t, root)
		_ = os.MkdirAll(filepath.Join(root, "home", "user"), 0o700)
		s.sandboxes[id] = &fakeSandbox{id: id, token: tok, root: root}
		s.counts.Created++
		s.lastAutoPause = createReq.AutoPause
		s.lastRunnerID = createReq.Metadata["dex_runner_id"]
		s.lastAutoResume = createReq.AutoResume != nil && createReq.AutoResume.Enabled
		s.mu.Unlock()
		writeTestJSON(w, http.StatusCreated, map[string]any{"sandboxID": id, "envdAccessToken": tok})
	}))
	apiMux.HandleFunc("POST /v2/sandboxes/{id}/connect", s.withAPIKey(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		s.mu.Lock()
		if s.connectUnsupported {
			s.mu.Unlock()
			writeTestJSON(w, http.StatusNotImplemented, map[string]string{"message": "connect not implemented"})
			return
		}
		sb := s.sandboxes[id]
		if sb == nil || sb.killed {
			s.mu.Unlock()
			writeTestJSON(w, http.StatusNotFound, map[string]string{"message": "sandbox not found"})
			return
		}
		sb.paused = false
		s.counts.Connected++
		tok := sb.token
		s.mu.Unlock()
		writeTestJSON(w, http.StatusOK, map[string]any{"sandboxID": id, "envdAccessToken": tok})
	}))
	apiMux.HandleFunc("GET /sandboxes/{id}", s.withAPIKey(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		s.mu.Lock()
		sb := s.sandboxes[id]
		if sb == nil || sb.killed {
			s.mu.Unlock()
			writeTestJSON(w, http.StatusNotFound, map[string]string{"message": "sandbox not found"})
			return
		}
		tok := sb.token
		s.mu.Unlock()
		writeTestJSON(w, http.StatusOK, map[string]any{"sandboxID": id, "envdAccessToken": tok})
	}))
	apiMux.HandleFunc("POST /sandboxes/{id}/timeout", s.withAPIKey(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		s.mu.Lock()
		sb := s.sandboxes[id]
		if sb == nil || sb.killed {
			s.mu.Unlock()
			writeTestJSON(w, http.StatusNotFound, map[string]string{"message": "sandbox not found"})
			return
		}
		s.counts.Renewed++
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	apiMux.HandleFunc("DELETE /sandboxes/{id}", s.withAPIKey(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		s.mu.Lock()
		sb := s.sandboxes[id]
		if sb == nil || sb.killed {
			s.mu.Unlock()
			writeTestJSON(w, http.StatusNotFound, map[string]string{"message": "sandbox not found"})
			return
		}
		sb.killed = true
		s.counts.Killed++
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))

	envdMux := http.NewServeMux()
	envdMux.HandleFunc("GET /envs", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.authSandbox(w, r); !ok {
			return
		}
		writeTestJSON(w, http.StatusOK, map[string]string{"HOME": "/home/user", "REMOTE_E2B_MARKER": "e2b-fake-ok"})
	})
	envdMux.HandleFunc("GET /files", s.handleDownload)
	envdMux.HandleFunc("POST /files", s.handleUpload)
	envdMux.HandleFunc("POST /filesystem.Filesystem/Stat", s.handleStat)
	envdMux.HandleFunc("POST /filesystem.Filesystem/MakeDir", s.handleMakeDir)
	envdMux.HandleFunc("POST /filesystem.Filesystem/ListDir", s.handleListDir)
	envdMux.HandleFunc("POST /filesystem.Filesystem/Remove", s.handleRemove)
	envdMux.HandleFunc("POST /process.Process/Start", s.handleProcessStart)
	envdMux.HandleFunc("POST /process.Process/SendSignal", s.handleSendSignal)

	s.apiMux = apiMux
	s.envdMux = envdMux
	s.apiSrv = httptest.NewServer(apiMux)
	s.envdSrv = httptest.NewServer(envdMux)
	t.Cleanup(func() {
		s.apiSrv.Close()
		s.envdSrv.Close()
	})
	return s
}

func (s *FakeE2BServer) APIURL() string  { return s.apiSrv.URL }
func (s *FakeE2BServer) EnvdURL() string { return s.envdSrv.URL }

// StartCombinedServer starts a single HTTP server routing both control-plane and envd endpoints,
// returning its base URL and a function that reports the last Host header seen by envd.
func (s *FakeE2BServer) StartCombinedServer(t *testing.T) (baseURL string, lastEnvdHost func() string) {
	t.Helper()
	var mu sync.Mutex
	var recordedHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v2/") || strings.HasPrefix(r.URL.Path, "/sandboxes/") {
			s.apiMux.ServeHTTP(w, r)
			return
		}
		mu.Lock()
		recordedHost = r.Host
		mu.Unlock()
		s.envdMux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() string {
		mu.Lock()
		defer mu.Unlock()
		return recordedHost
	}
}

func (s *FakeE2BServer) SetConnectUnsupported(v bool) {
	s.mu.Lock()
	s.connectUnsupported = v
	s.mu.Unlock()
}

func (s *FakeE2BServer) LastCreateAutoPause() (autoPause, autoResume bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAutoPause, s.lastAutoResume
}

func (s *FakeE2BServer) LastCreateRunnerID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRunnerID
}

func (s *FakeE2BServer) Counts() FakeE2BCounts {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts
}

// SimulateAutoPause marks sandboxID paused (simulating E2B's server-side idle autoPause).
func (s *FakeE2BServer) SimulateAutoPause(sandboxID string) {
	s.mu.Lock()
	if sb := s.sandboxes[sandboxID]; sb != nil {
		sb.paused = true
	}
	s.mu.Unlock()
}

// SimulateExpire removes sandboxID (simulating expired sandbox returning 404 on /connect).
func (s *FakeE2BServer) SimulateExpire(sandboxID string) {
	s.mu.Lock()
	if sb := s.sandboxes[sandboxID]; sb != nil {
		sb.killed = true
	}
	s.mu.Unlock()
}

func (s *FakeE2BServer) withAPIKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.apiKey == "" {
			if len(r.Header.Values("X-API-Key")) > 0 {
				writeTestJSON(w, http.StatusUnauthorized, map[string]string{"message": "unexpected api key"})
				return
			}
			next(w, r)
			return
		}
		if r.Header.Get("X-API-Key") != s.apiKey {
			writeTestJSON(w, http.StatusUnauthorized, map[string]string{"message": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func (s *FakeE2BServer) authSandbox(w http.ResponseWriter, r *http.Request) (*fakeSandbox, bool) {
	id := r.Header.Get("E2b-Sandbox-Id")
	s.mu.Lock()
	sb := s.sandboxes[id]
	invalid := sb == nil || sb.killed || sb.paused || r.Header.Get("X-Access-Token") != sb.token
	s.mu.Unlock()
	if invalid {
		writeTestJSON(w, http.StatusBadGateway, map[string]string{"code": "unavailable", "message": "sandbox unavailable"})
		return nil, false
	}
	return sb, true
}

func hostTarget(sb *fakeSandbox, raw string) (string, string) {
	clean := path.Clean("/" + strings.TrimSpace(raw))
	return clean, filepath.Join(sb.root, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
}

func (s *FakeE2BServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.authSandbox(w, r)
	if !ok {
		return
	}
	_, target := hostTarget(sb, r.URL.Query().Get("path"))
	data, err := os.ReadFile(target) //nolint:gosec // confined inside sandbox temp root.
	if err != nil {
		writeEnvdErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	if rng := r.Header.Get("Range"); strings.HasPrefix(rng, "bytes=") {
		parts := strings.SplitN(strings.TrimPrefix(rng, "bytes="), "-", 2)
		if len(parts) == 2 {
			start, _ := strconv.ParseInt(parts[0], 10, 64)
			end, _ := strconv.ParseInt(parts[1], 10, 64)
			if start >= int64(len(data)) {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			end = min(end+1, int64(len(data)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[start:end]) //nolint:gosec // raw binary download in test fake.
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data) //nolint:gosec // raw binary download in test fake.
}

func (s *FakeE2BServer) handleUpload(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.authSandbox(w, r)
	if !ok {
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeTestJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_argument", "message": err.Error()})
		return
	}
	part, err := mr.NextPart()
	if err != nil {
		writeTestJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_argument", "message": err.Error()})
		return
	}
	data, err := io.ReadAll(part)
	if err != nil {
		writeTestJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_argument", "message": err.Error()})
		return
	}
	clean, _ := hostTarget(sb, r.URL.Query().Get("path"))
	rel := filepath.FromSlash(strings.TrimPrefix(clean, "/"))
	root, err := os.OpenRoot(sb.root)
	if err != nil {
		writeEnvdErr(w, err)
		return
	}
	defer func() { _ = root.Close() }()
	if parent := filepath.Dir(rel); parent != "." {
		if err := root.MkdirAll(parent, 0o700); err != nil {
			writeEnvdErr(w, err)
			return
		}
	}
	if err := root.WriteFile(rel, data, 0o600); err != nil {
		writeEnvdErr(w, err)
		return
	}
	writeTestJSON(w, http.StatusOK, []any{})
}

func (s *FakeE2BServer) handleStat(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.authSandbox(w, r)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	_ = json.UnmarshalRead(r.Body, &req)
	clean, target := hostTarget(sb, req.Path)
	entry, err := statEntry(sb, clean, target)
	if err != nil {
		writeEnvdErr(w, err)
		return
	}
	writeTestJSON(w, http.StatusOK, map[string]any{"entry": entry})
}

func (s *FakeE2BServer) handleMakeDir(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.authSandbox(w, r)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	_ = json.UnmarshalRead(r.Body, &req)
	clean, target := hostTarget(sb, req.Path)
	if err := os.MkdirAll(target, 0o700); err != nil {
		writeEnvdErr(w, err)
		return
	}
	entry, _ := statEntry(sb, clean, target)
	writeTestJSON(w, http.StatusOK, map[string]any{"entry": entry})
}

func (s *FakeE2BServer) handleListDir(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.authSandbox(w, r)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	_ = json.UnmarshalRead(r.Body, &req)
	clean, target := hostTarget(sb, req.Path)
	items, err := os.ReadDir(target)
	if err != nil {
		writeEnvdErr(w, err)
		return
	}
	entries := make([]map[string]any, 0, len(items))
	for _, item := range items {
		childClean := path.Join(clean, item.Name())
		if entry, sErr := statEntry(sb, childClean, filepath.Join(target, item.Name())); sErr == nil {
			entries = append(entries, entry)
		}
	}
	writeTestJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *FakeE2BServer) handleRemove(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.authSandbox(w, r)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	_ = json.UnmarshalRead(r.Body, &req)
	_, target := hostTarget(sb, req.Path)
	if err := os.RemoveAll(target); err != nil {
		writeEnvdErr(w, err)
		return
	}
	writeTestJSON(w, http.StatusOK, map[string]any{})
}

func (s *FakeE2BServer) handleSendSignal(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authSandbox(w, r); !ok {
		return
	}
	var req struct {
		Process struct {
			PID uint32 `json:"pid"`
		} `json:"process"`
	}
	_ = json.UnmarshalRead(r.Body, &req)
	s.mu.Lock()
	cmd := s.procs[req.Process.PID]
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	writeTestJSON(w, http.StatusOK, map[string]any{})
}

func (s *FakeE2BServer) handleProcessStart(w http.ResponseWriter, r *http.Request) {
	sb, ok := s.authSandbox(w, r)
	if !ok {
		return
	}
	var hdr [5]byte
	if _, err := io.ReadFull(r.Body, hdr[:]); err != nil {
		writeTestJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_argument", "message": err.Error()})
		return
	}
	payload := make([]byte, binary.BigEndian.Uint32(hdr[1:5]))
	if _, err := io.ReadFull(r.Body, payload); err != nil {
		writeTestJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_argument", "message": err.Error()})
		return
	}
	var req struct {
		Process struct {
			Cmd  string            `json:"cmd"`
			Args []string          `json:"args"`
			Envs map[string]string `json:"envs"`
			Cwd  string            `json:"cwd"`
		} `json:"process"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		writeTestJSON(w, http.StatusBadRequest, map[string]string{"code": "invalid_argument", "message": err.Error()})
		return
	}

	_, hostCwd := hostTarget(sb, req.Process.Cwd)
	args := rewriteSandboxArgs(sb, req.Process.Args)
	cmd := exec.CommandContext(r.Context(), req.Process.Cmd, args...) //nolint:gosec // isolated test command.
	cmd.Dir = hostCwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + filepath.Join(sb.root, "home", "user"),
		"REMOTE_E2B_MARKER=e2b-fake-ok",
	}
	for k, v := range req.Process.Envs {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	w.Header().Set("Content-Type", "application/connect+json")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	var streamMu sync.Mutex
	sendFrame := func(flags byte, msg any) {
		b, _ := json.Marshal(msg)
		frame := make([]byte, 5+len(b))
		frame[0] = flags
		binary.BigEndian.PutUint32(frame[1:5], uint32(len(b))) //nolint:gosec // bounded test frame.
		copy(frame[5:], b)
		streamMu.Lock()
		_, _ = w.Write(frame)
		if flusher != nil {
			flusher.Flush()
		}
		streamMu.Unlock()
	}

	if err := cmd.Start(); err != nil {
		sendFrame(0x02, map[string]any{"error": map[string]string{"code": "internal", "message": err.Error()}})
		return
	}
	pid := uint32(cmd.Process.Pid) //nolint:gosec // positive OS PID.
	s.mu.Lock()
	s.procs[pid] = cmd
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.procs, pid)
		s.mu.Unlock()
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}()

	sendFrame(0x00, map[string]any{"event": map[string]any{"start": map[string]uint32{"pid": pid}}})

	var wg sync.WaitGroup
	pump := func(rd io.Reader, field string) {
		defer wg.Done()
		buf := make([]byte, 16*1024)
		for {
			n, rErr := rd.Read(buf)
			if n > 0 {
				sendFrame(0x00, map[string]any{"event": map[string]any{"data": map[string][]byte{field: buf[:n]}}})
			}
			if rErr != nil {
				return
			}
		}
	}
	wg.Add(2)
	go pump(stdout, "stdout")
	go pump(stderr, "stderr")
	wg.Wait()

	exitCode := 0
	if waitErr := cmd.Wait(); waitErr != nil {
		if ee, ok := errors.AsType[*exec.ExitError](waitErr); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = 1
		}
	}
	sendFrame(0x00, map[string]any{"event": map[string]any{"end": map[string]any{"exitCode": exitCode, "exited": true}}})
	sendFrame(0x02, map[string]any{})
}

// rewriteSandboxArgs maps absolute /home/user/... paths in bash -c / positional args into sb.root for local test execution.
func rewriteSandboxArgs(sb *fakeSandbox, args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.HasPrefix(a, "/home/user") || strings.HasPrefix(a, "/tmp/pi-output-") {
			_, mapped := hostTarget(sb, a)
			out[i] = mapped
		} else {
			out[i] = strings.ReplaceAll(a, "/home/user", filepath.Join(sb.root, "home", "user"))
		}
	}
	return out
}

func statEntry(sb *fakeSandbox, cleanPath, target string) (map[string]any, error) {
	info, err := os.Lstat(target)
	if err != nil {
		return nil, err
	}
	kind := "FILE_TYPE_FILE"
	var symTarget *string
	if info.Mode()&os.ModeSymlink != 0 {
		kind = "FILE_TYPE_SYMLINK"
		if dest, lErr := os.Readlink(target); lErr == nil {
			// Map back inside sandbox if pointing into sb.root.
			if rel, rErr := filepath.Rel(sb.root, dest); rErr == nil && !strings.HasPrefix(rel, "..") {
				dest = "/" + filepath.ToSlash(rel)
			}
			symTarget = &dest
		}
	} else if info.IsDir() {
		kind = "FILE_TYPE_DIRECTORY"
	}
	entry := map[string]any{
		"name":         path.Base(cleanPath),
		"type":         kind,
		"path":         cleanPath,
		"size":         strconv.FormatInt(info.Size(), 10),
		"modifiedTime": info.ModTime().UTC(),
	}
	if symTarget != nil {
		entry["symlinkTarget"] = *symTarget
	}
	return entry, nil
}

func writeEnvdErr(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "internal"
	if errors.Is(err, os.ErrNotExist) {
		status, code = http.StatusNotFound, "not_found"
	} else if errors.Is(err, os.ErrPermission) {
		status, code = http.StatusForbidden, "permission_denied"
	}
	writeTestJSON(w, status, map[string]string{"code": code, "message": err.Error()})
}

func writeTestJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(v)
	_, _ = w.Write(b)
}
