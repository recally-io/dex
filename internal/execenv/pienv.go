package execenv

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"uuid"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	pienv "github.com/MichaelKinsy/PiG/env"
)

const (
	defaultPiEnvWorkspaceBase = "~/.dex/agents"
	pigModulePath             = "github.com/MichaelKinsy/PiG"
	piEnvDaemonSubpkg         = "./env/cmd/pi-env"
	remoteLoginEnvMarker      = "__DEX_LOGIN_ENV__"
)

var validRemoteEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// PiEnvConfig holds stored JSON settings for an SSH execution environment.
type PiEnvConfig struct {
	Host         string `json:"host,omitempty"`
	User         string `json:"user,omitempty"`
	Port         *int   `json:"port,omitempty"`
	IdentityFile string `json:"identityFile,omitempty"`
	LoginShell   *bool  `json:"loginShell,omitempty"`
	WorkspaceDir string `json:"workspaceDir,omitempty"`
}

// LoginShellEnabled reports whether SSH sessions use the remote user's login shell (default true).
func (c PiEnvConfig) LoginShellEnabled() bool {
	return c.LoginShell == nil || *c.LoginShell
}

// ParsePiEnvConfig decodes and normalizes raw JSON configuration for SSH environments.
func ParsePiEnvConfig(raw string) (PiEnvConfig, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		trimmed = "{}"
	}
	var cfg PiEnvConfig
	if err := json.Unmarshal([]byte(trimmed), &cfg); err != nil {
		return PiEnvConfig{}, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.User = strings.TrimSpace(cfg.User)
	cfg.IdentityFile = strings.TrimSpace(cfg.IdentityFile)
	cfg.WorkspaceDir = strings.TrimSpace(cfg.WorkspaceDir)
	if cfg.Port != nil {
		cfg.Port = new(*cfg.Port)
	}
	if cfg.LoginShell != nil {
		cfg.LoginShell = new(*cfg.LoginShell)
	}
	return cfg, nil
}

// ValidatePiEnvConfig validates an SSH pi-env configuration.
func ValidatePiEnvConfig(dataDir, envID string, cfg PiEnvConfig, sshProgram string) error {
	if cfg.Host == "" {
		return fmt.Errorf("%w: host is required", ErrInvalidConfig)
	}
	if cfg.WorkspaceDir != "" {
		if strings.ContainsAny(cfg.WorkspaceDir, "\x00\r\n") {
			return fmt.Errorf("%w: workspaceDir contains invalid characters", ErrInvalidConfig)
		}
		if cfg.WorkspaceDir != "~" && !strings.HasPrefix(cfg.WorkspaceDir, "~/") && !IsAbsPath(cfg.WorkspaceDir) {
			return fmt.Errorf("%w: workspaceDir must be an absolute path or start with ~", ErrInvalidConfig)
		}
	}
	target := sshTarget(dataDir, envID, cfg, sshProgram)
	if _, err := pienv.SshArguments(target); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	return nil
}

func forgetPiEnvHostKey(dataDir, envID string) error {
	if dataDir == "" || envID == "" {
		return nil
	}
	return pienv.ForgetHostKey(sshTarget(dataDir, envID, PiEnvConfig{Host: "placeholder"}, ""))
}

func samePiEnvEndpoint(a, b PiEnvConfig) bool {
	var portA, portB int
	if a.Port != nil {
		portA = *a.Port
	}
	if b.Port != nil {
		portB = *b.Port
	}
	return a.Host == b.Host && portA == portB
}

func sshTarget(dataDir, envID string, cfg PiEnvConfig, sshProgram string) pienv.SshTarget {
	return sshTargetWithKnownHosts(
		filepath.Join(dataDir, "ssh_known_hosts"),
		"dex-pi-env-"+envID,
		cfg,
		sshProgram,
	)
}

func sshTargetWithKnownHosts(knownHostsFile, hostKeyAlias string, cfg PiEnvConfig, sshProgram string) pienv.SshTarget {
	target := pienv.SshTarget{
		Host:           cfg.Host,
		Port:           cfg.Port,
		KnownHostsFile: knownHostsFile,
		HostKeyAlias:   hostKeyAlias,
	}
	if cfg.User != "" {
		target.User = new(cfg.User)
	}
	if cfg.IdentityFile != "" {
		target.IdentityFile = new(cfg.IdentityFile)
	}
	if sshProgram != "" {
		target.Ssh = new(sshProgram)
	}
	return target
}

type piEnvRuntime struct {
	baseDriver
	dataDir    string
	sshProgram string
	buildMu    sync.Mutex
}

func (r *piEnvRuntime) normalizeAndValidate(_ context.Context, spec EnvironmentSpec) (string, error) {
	cfg, err := ParsePiEnvConfig(spec.Config)
	if err != nil {
		return "", err
	}
	if err := ValidatePiEnvConfig(r.dataDir, cmp.Or(spec.ID, "probe"), cfg, r.sshProgram); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (r *piEnvRuntime) open(ctx context.Context, agentID string, spec EnvironmentSpec) (Handle, error) {
	cfg, err := ParsePiEnvConfig(spec.Config)
	if err != nil {
		return nil, err
	}
	if err := ValidatePiEnvConfig(r.dataDir, spec.ID, cfg, r.sshProgram); err != nil {
		return nil, err
	}
	target := sshTarget(r.dataDir, spec.ID, cfg, r.sshProgram)
	remote, err := r.detectWithTOFU(ctx, target)
	if err != nil {
		return nil, err
	}
	binary, err := r.resolveDaemon(ctx, remote)
	if err != nil {
		return nil, err
	}
	loginShell := cfg.LoginShellEnabled()
	conn, detected, err := pienv.ConnectSsh(ctx, pienv.SshConnectOptions{
		SshTarget:  target,
		Binary:     &binary,
		LoginShell: loginShell,
	})
	if err != nil {
		return nil, err
	}
	sep := "/"
	if detected.Platform == "windows" {
		sep = `\`
	}
	baseWS, err := resolveRemoteWorkspaceBase(detected.Home, sep, cfg.WorkspaceDir)
	if err != nil {
		conn.Close()
		return nil, err
	}
	workspace := joinRemoteSegments(baseWS, sep, agentID, "workspace")
	var loginEnv map[string]string
	if loginShell && detected.Platform != "windows" {
		loginEnv = captureRemoteLoginEnv(ctx, conn, detected.Home)
	}
	h := &piEnvHandle{
		id:        agentID,
		workspace: workspace,
		home:      detected.Home,
		conn:      conn,
		loginEnv:  loginEnv,
	}
	env, err := h.Env(ctx, workspace, nil)
	if err != nil {
		_ = h.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	if err := env.CreateDir(ctx, workspace, &durableenv.CreateDirOptions{Recursive: new(true)}); err != nil {
		_ = h.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	return h, nil
}

func (r *piEnvRuntime) probe(ctx context.Context, saved *EnvironmentSpec, spec EnvironmentSpec) (TestResult, error) {
	cfg, err := ParsePiEnvConfig(spec.Config)
	if err != nil {
		return TestResult{}, err
	}
	ephemeralKnownHosts := true
	envID := ""
	if saved != nil && saved.Kind == KindPiEnv && saved.ID != "" {
		if oldCfg, parseErr := ParsePiEnvConfig(saved.Config); parseErr == nil && samePiEnvEndpoint(oldCfg, cfg) {
			ephemeralKnownHosts = false
			envID = saved.ID
		}
	}
	probeAlias := cmp.Or(envID, "probe")
	if err := ValidatePiEnvConfig(r.dataDir, probeAlias, cfg, r.sshProgram); err != nil {
		return TestResult{}, err
	}

	var target pienv.SshTarget
	if ephemeralKnownHosts || envID == "" {
		tmpDir, err := os.MkdirTemp("", "dex-ssh-probe-*")
		if err != nil {
			return TestResult{}, err
		}
		defer func() { _ = os.RemoveAll(tmpDir) }()
		target = sshTargetWithKnownHosts(filepath.Join(tmpDir, "known_hosts"), "dex-pi-env-probe", cfg, r.sshProgram)
	} else {
		if err := os.MkdirAll(r.dataDir, 0o700); err != nil {
			return TestResult{}, err
		}
		target = sshTarget(r.dataDir, envID, cfg, r.sshProgram)
	}

	remote, err := r.detectWithTOFU(ctx, target)
	if err != nil {
		return TestResult{OK: false, HostKeyChanged: isHostKeyChanged(err), Error: err.Error()}, nil
	}
	binary, err := r.resolveDaemon(ctx, remote)
	if err != nil {
		return TestResult{OK: false, Platform: remote.Platform, Arch: remote.Arch, Home: remote.Home, Error: err.Error()}, nil
	}
	conn, detected, err := pienv.ConnectSsh(ctx, pienv.SshConnectOptions{
		SshTarget:  target,
		Binary:     &binary,
		LoginShell: cfg.LoginShellEnabled(),
	})
	if err != nil {
		return TestResult{
			OK:             false,
			Platform:       remote.Platform,
			Arch:           remote.Arch,
			Home:           remote.Home,
			HostKeyChanged: isHostKeyChanged(err),
			Error:          err.Error(),
		}, nil
	}
	defer conn.Close()

	sep := "/"
	if detected.Platform == "windows" {
		sep = `\`
	}
	baseWS, err := resolveRemoteWorkspaceBase(detected.Home, sep, cfg.WorkspaceDir)
	if err != nil {
		return TestResult{OK: false, Platform: detected.Platform, Arch: detected.Arch, Home: detected.Home, Error: err.Error()}, nil
	}
	env := pienv.NewRemoteExecutionEnv(pienv.RemoteExecutionEnvOptions{
		Connection: conn,
		ID:         "pi-env:test:" + probeAlias,
		Cwd:        detected.Home,
	})
	if err := env.CreateDir(ctx, baseWS, &durableenv.CreateDirOptions{Recursive: new(true)}); err != nil {
		return TestResult{OK: false, Platform: detected.Platform, Arch: detected.Arch, Home: detected.Home, Error: err.Error()}, nil
	}
	return TestResult{
		OK:       true,
		Platform: detected.Platform,
		Arch:     detected.Arch,
		Home:     detected.Home,
		Warnings: detected.Warnings,
	}, nil
}

func (r *piEnvRuntime) onUpdated(oldSpec, newSpec EnvironmentSpec) error {
	if r.dataDir == "" || newSpec.ID == "" {
		return nil
	}
	oldCfg, err := ParsePiEnvConfig(oldSpec.Config)
	if err != nil {
		return nil //nolint:nilerr // malformed prior config should still clear host key on endpoint update.
	}
	newCfg, err := ParsePiEnvConfig(newSpec.Config)
	if err != nil || !samePiEnvEndpoint(oldCfg, newCfg) {
		return forgetPiEnvHostKey(r.dataDir, newSpec.ID)
	}
	return nil
}

func (r *piEnvRuntime) onDeleted(spec EnvironmentSpec) error {
	if r.dataDir == "" || spec.ID == "" {
		return nil
	}
	return forgetPiEnvHostKey(r.dataDir, spec.ID)
}

func (r *piEnvRuntime) forgetHostKey(spec EnvironmentSpec) error {
	if r.dataDir == "" || strings.TrimSpace(spec.ID) == "" {
		return nil
	}
	return forgetPiEnvHostKey(r.dataDir, spec.ID)
}

func captureRemoteLoginEnv(ctx context.Context, conn *pienv.Connection, home string) map[string]string {
	probeCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	env := pienv.NewRemoteExecutionEnv(pienv.RemoteExecutionEnvOptions{
		Connection: conn,
		ID:         "pi-env:login-env",
		Cwd:        cmp.Or(home, "/"),
	})
	var (
		mu     sync.Mutex
		stdout bytes.Buffer
	)
	res, err := env.Exec(probeCtx, []string{
		"sh", "-c", `exec "${SHELL:-/bin/sh}" -l -i -c 'printf "%s" ` + remoteLoginEnvMarker + `; env -0'`,
	}, &durableenv.ShellExecOptions{
		Timeout:    new(5.0),
		InheritEnv: new(true),
		OnOutput: func(_ context.Context, text string, info durableenv.ShellOutputInfo) {
			if info.Stream == durableenv.ShellStdout {
				mu.Lock()
				stdout.WriteString(text)
				mu.Unlock()
			}
		},
	})
	if err != nil || res.ExitCode != 0 {
		return nil
	}
	mu.Lock()
	rawOut := bytes.Clone(stdout.Bytes())
	mu.Unlock()
	_, data, ok := bytes.Cut(rawOut, []byte(remoteLoginEnvMarker))
	if !ok {
		return nil
	}
	parsed := map[string]string{}
	for item := range bytes.SplitSeq(data, []byte{0}) {
		key, value, hasEq := strings.Cut(string(item), "=")
		if !hasEq || !validRemoteEnvName.MatchString(key) {
			continue
		}
		switch key {
		case "_", "PWD", "OLDPWD", "SHLVL":
			continue
		}
		parsed[key] = value
	}
	if len(parsed) == 0 {
		return nil
	}
	return parsed
}

func isHostKeyChanged(err error) bool {
	_, changed := errors.AsType[*pienv.HostKeyChangedError](err)
	return changed
}

func (r *piEnvRuntime) detectWithTOFU(ctx context.Context, target pienv.SshTarget) (pienv.RemotePlatform, error) {
	if err := os.MkdirAll(filepath.Dir(target.KnownHostsFile), 0o700); err != nil {
		return pienv.RemotePlatform{}, err
	}
	remote, err := pienv.DetectPlatform(ctx, target)
	if err == nil {
		return remote, nil
	}
	if _, unknown := errors.AsType[*pienv.HostKeyUnknownError](err); !unknown {
		return pienv.RemotePlatform{}, err
	}
	lines, _, scanErr := pienv.ScanHostKey(ctx, target)
	if scanErr != nil {
		return pienv.RemotePlatform{}, scanErr
	}
	if acceptErr := pienv.AcceptHostKey(target, lines); acceptErr != nil {
		return pienv.RemotePlatform{}, acceptErr
	}
	return pienv.DetectPlatform(ctx, target)
}

func (r *piEnvRuntime) resolveDaemon(ctx context.Context, remote pienv.RemotePlatform) (string, error) {
	packaged := pienv.PackagedDaemon(remote)
	if info, err := os.Stat(packaged); err == nil && !info.IsDir() && info.Size() > 0 {
		return packaged, nil
	}
	goos, goarch, binName, err := daemonBuildTarget(remote)
	if err != nil {
		return "", err
	}
	cacheVer := cmp.Or(pigVersionFromBuildInfo(), "dev")
	cachePath := filepath.Join(r.dataDir, "cache", "pi-env", cacheVer, remote.Platform+"-"+remote.Arch, binName)

	r.buildMu.Lock()
	defer r.buildMu.Unlock()
	if info, err := os.Stat(cachePath); err == nil && !info.IsDir() && info.Size() > 0 {
		return cachePath, nil
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		return "", fmt.Errorf("pi-env daemon missing at %s and go not found on PATH: %w", packaged, err)
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		return "", err
	}
	tmpPath := cachePath + ".tmp." + uuid.NewV7().String()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := buildPiEnvDaemon(ctx, goBin, goos, goarch, tmpPath); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpPath, 0o700); err != nil { //nolint:gosec // G302: cached daemon binary under user data directory must be owner-executable.
		return "", err
	}
	if err := os.Rename(tmpPath, cachePath); err != nil {
		return "", err
	}
	return cachePath, nil
}

func daemonBuildTarget(remote pienv.RemotePlatform) (goos, goarch, binName string, err error) {
	switch remote.Platform {
	case "linux", "android", "darwin":
		binName, goos = "pi-env", remote.Platform
	case "windows":
		binName, goos = "pi-env.exe", remote.Platform
	default:
		return "", "", "", fmt.Errorf("unsupported pi-env remote platform %q", remote.Platform)
	}
	switch remote.Arch {
	case "x64", "amd64":
		goarch = "amd64"
	case "arm64", "aarch64":
		goarch = "arm64"
	default:
		return "", "", "", fmt.Errorf("unsupported pi-env remote architecture %q", remote.Arch)
	}
	return goos, goarch, binName, nil
}

func buildPiEnvDaemon(ctx context.Context, goBin, goos, goarch, outPath string) error {
	absOutPath, err := filepath.Abs(outPath)
	if err != nil {
		return err
	}
	pigDir, err := locatePiGModuleDir(ctx, goBin)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, goBin, "build", "-mod=readonly", "-trimpath", "-ldflags=-s -w", "-o", absOutPath, piEnvDaemonSubpkg) //nolint:gosec // goBin comes from exec.LookPath("go") with fixed arguments.
	cmd.Dir = pigDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOTOOLCHAIN=local", "GOOS="+goos, "GOARCH="+goarch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build pi-env daemon (%s/%s): %w: %s", goos, goarch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func locatePiGModuleDir(ctx context.Context, goBin string) (string, error) {
	listCmd := exec.CommandContext(ctx, goBin, "list", "-m", "-f", "{{.Dir}}", pigModulePath) //nolint:gosec // goBin comes from exec.LookPath("go").
	listCmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	if out, err := listCmd.Output(); err == nil {
		if dir := strings.TrimSpace(string(out)); dir != "" {
			if _, statErr := os.Stat(filepath.Join(dir, "env", "cmd", "pi-env")); statErr == nil {
				return dir, nil
			}
		}
	}
	ver := pigVersionFromBuildInfo()
	if ver == "" {
		return "", fmt.Errorf("cannot locate %s module for pi-env daemon build", pigModulePath)
	}
	dlCmd := exec.CommandContext(ctx, goBin, "mod", "download", "-json", pigModulePath+"@"+ver) //nolint:gosec // fixed module path and build-info version.
	dlCmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	out, dlErr := dlCmd.CombinedOutput()
	var mod struct {
		Dir   string `json:"Dir"`
		Error string `json:"Error"`
	}
	if err := json.Unmarshal(out, &mod); err == nil {
		if mod.Error != "" {
			return "", fmt.Errorf("locate %s@%s: %s", pigModulePath, ver, mod.Error)
		}
		if mod.Dir != "" {
			return mod.Dir, nil
		}
	}
	if dlErr != nil {
		return "", fmt.Errorf("locate %s@%s: %w: %s", pigModulePath, ver, dlErr, strings.TrimSpace(string(out)))
	}
	return "", fmt.Errorf("cannot locate %s@%s directory", pigModulePath, ver)
}

func pigVersionFromBuildInfo() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, dep := range info.Deps {
		if dep.Path == pigModulePath {
			if dep.Replace != nil && dep.Replace.Version != "" {
				return dep.Replace.Version
			}
			return dep.Version
		}
	}
	return ""
}

func resolveRemoteWorkspaceBase(home, sep, configured string) (string, error) {
	raw := cmp.Or(strings.TrimSpace(configured), defaultPiEnvWorkspaceBase)
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		if home == "" {
			return "", errors.New("remote home directory is empty")
		}
		if raw == "~" {
			return cleanRemoteAbsPath(home, sep), nil
		}
		return joinRemoteSegments(home, sep, strings.TrimPrefix(raw, "~/")), nil
	}
	if !IsAbsPath(raw) {
		return "", fmt.Errorf("%w: workspaceDir must be absolute or start with ~", ErrInvalidConfig)
	}
	return cleanRemoteAbsPath(raw, sep), nil
}

func joinRemoteSegments(base, sep string, parts ...string) string {
	if sep == `\` {
		var out strings.Builder
		out.WriteString(strings.TrimRight(strings.ReplaceAll(base, "/", `\`), `\`))
		for _, p := range parts {
			if seg := strings.Trim(strings.ReplaceAll(p, "/", `\`), `\`); seg != "" {
				out.WriteString(`\` + seg)
			}
		}
		return out.String()
	}
	return path.Join(append([]string{base}, parts...)...)
}

func cleanRemoteAbsPath(p, sep string) string {
	if sep == `\` {
		return strings.TrimRight(strings.ReplaceAll(p, "/", `\`), `\`)
	}
	return path.Clean(p)
}

type piEnvHandle struct {
	id        string
	workspace string
	home      string
	conn      *pienv.Connection
	loginEnv  map[string]string
	closeOnce sync.Once
}

func (h *piEnvHandle) Kind() Kind                               { return KindPiEnv }
func (h *piEnvHandle) ID() string                               { return h.id }
func (h *piEnvHandle) Workspace() string                        { return h.workspace }
func (h *piEnvHandle) Home(ctx context.Context) (string, error) { return h.home, ctx.Err() }

func (h *piEnvHandle) Env(ctx context.Context, cwd string, extraEnv map[string]string) (durableenv.ExecutionEnv, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	merged := maps.Clone(h.loginEnv)
	if len(extraEnv) > 0 {
		if merged == nil {
			merged = make(map[string]string, len(extraEnv))
		}
		maps.Copy(merged, extraEnv)
	}
	return pienv.NewRemoteExecutionEnv(pienv.RemoteExecutionEnvOptions{
		Connection: h.conn,
		ID:         "pi-env:" + h.id,
		Cwd:        cmp.Or(cwd, h.workspace),
		ShellEnv:   merged,
	}), nil
}

func (h *piEnvHandle) Close(context.Context) error {
	h.closeOnce.Do(h.conn.Close)
	return nil
}
