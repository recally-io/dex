package execenv

import (
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
	"runtime/debug"
	"strings"
	"sync"
	"uuid"

	durableenv "github.com/MichaelKinsy/PiG/durable/env"
	pienv "github.com/MichaelKinsy/PiG/env"
)

const (
	defaultPiEnvWorkspaceBase = "~/.dex/agents"
	pigModulePath             = "github.com/MichaelKinsy/PiG"
	piEnvCmdPkg               = pigModulePath + "/cmd/pi-env"
)

// PiEnvConfig holds stored JSON settings for a pi-env SSH execution environment.
type PiEnvConfig struct {
	Host         string `json:"host,omitempty"`
	User         string `json:"user,omitempty"`
	Port         *int   `json:"port,omitempty"`
	IdentityFile string `json:"identityFile,omitempty"`
	WorkspaceDir string `json:"workspaceDir,omitempty"`
}

// ParsePiEnvConfig decodes and normalizes raw JSON configuration for pi-env.
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
		p := *cfg.Port
		cfg.Port = &p
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

// ForgetPiEnvHostKey clears any stored SSH host keys for envID.
func ForgetPiEnvHostKey(dataDir, envID string) error {
	if dataDir == "" || envID == "" {
		return nil
	}
	return pienv.ForgetHostKey(sshTarget(dataDir, envID, PiEnvConfig{Host: "placeholder"}, ""))
}

func sshTarget(dataDir, envID string, cfg PiEnvConfig, sshProgram string) pienv.SshTarget {
	target := pienv.SshTarget{
		Host:           cfg.Host,
		Port:           cfg.Port,
		KnownHostsFile: filepath.Join(dataDir, "ssh_known_hosts"),
		HostKeyAlias:   "dex-pi-env-" + envID,
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
	dataDir    string
	sshProgram string
	buildMu    sync.Mutex
}

func (r *piEnvRuntime) open(ctx context.Context, agentID, envID string, cfg PiEnvConfig) (Handle, error) {
	if err := ValidatePiEnvConfig(r.dataDir, envID, cfg, r.sshProgram); err != nil {
		return nil, err
	}
	target := sshTarget(r.dataDir, envID, cfg, r.sshProgram)
	remote, err := r.detectWithTOFU(ctx, target)
	if err != nil {
		return nil, err
	}
	binary, err := r.resolveDaemon(ctx, remote)
	if err != nil {
		return nil, err
	}
	conn, detected, err := pienv.ConnectSsh(ctx, pienv.SshConnectOptions{
		SshTarget:  target,
		Binary:     &binary,
		LoginShell: true,
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
	h := &piEnvHandle{
		id:        agentID,
		workspace: workspace,
		home:      detected.Home,
		conn:      conn,
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

func (r *piEnvRuntime) test(ctx context.Context, envID string, cfg PiEnvConfig) (TestResult, error) {
	if err := ValidatePiEnvConfig(r.dataDir, envID, cfg, r.sshProgram); err != nil {
		return TestResult{}, err
	}
	if err := os.MkdirAll(r.dataDir, 0o700); err != nil {
		return TestResult{}, err
	}
	target := sshTarget(r.dataDir, envID, cfg, r.sshProgram)
	remote, err := r.detectWithTOFU(ctx, target)
	if err != nil {
		return TestResult{OK: false, Error: err.Error()}, nil
	}
	binary, err := r.resolveDaemon(ctx, remote)
	if err != nil {
		return TestResult{OK: false, Platform: remote.Platform, Arch: remote.Arch, Home: remote.Home, Error: err.Error()}, nil
	}
	conn, detected, err := pienv.ConnectSsh(ctx, pienv.SshConnectOptions{
		SshTarget:  target,
		Binary:     &binary,
		LoginShell: true,
	})
	if err != nil {
		return TestResult{OK: false, Platform: remote.Platform, Arch: remote.Arch, Home: remote.Home, Error: err.Error()}, nil
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
		ID:         "pi-env:test:" + envID,
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
	cachePath := filepath.Join(r.dataDir, "cache", "pi-env", remote.Platform+"-"+remote.Arch, binName)

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
	workDir := ""
	if exec.CommandContext(ctx, goBin, "list", "-m", pigModulePath).Run() != nil { //nolint:gosec // goBin comes from exec.LookPath("go").
		ver := pigVersionFromBuildInfo()
		if ver == "" {
			return fmt.Errorf("cannot locate %s module for pi-env daemon build", pigModulePath)
		}
		tmpMod, err := os.MkdirTemp("", "dex-pi-env-build-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(tmpMod) }()
		goMod := fmt.Sprintf("module dex-pi-env-build\n\ngo 1.27\n\nrequire %s %s\n", pigModulePath, ver)
		if err := os.WriteFile(filepath.Join(tmpMod, "go.mod"), []byte(goMod), 0o600); err != nil {
			return err
		}
		workDir = tmpMod
	}
	cmd := exec.CommandContext(ctx, goBin, "build", "-trimpath", "-ldflags=-s -w", "-o", outPath, piEnvCmdPkg) //nolint:gosec // fixed build arguments.
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build pi-env daemon (%s/%s): %w: %s", goos, goarch, err, strings.TrimSpace(string(out)))
	}
	return nil
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
	return pienv.NewRemoteExecutionEnv(pienv.RemoteExecutionEnvOptions{
		Connection: h.conn,
		ID:         "pi-env:" + h.id,
		Cwd:        cmp.Or(cwd, h.workspace),
		ShellEnv:   maps.Clone(extraEnv),
	}), nil
}

func (h *piEnvHandle) Close(context.Context) error {
	h.closeOnce.Do(h.conn.Close)
	return nil
}
