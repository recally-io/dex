package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/update"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

const (
	maxRunnerArchiveBytes = 256 << 20
	maxRunnerChecksums    = 8 << 20
)

var (
	buildVersion string
	reexecAgent  = update.Reexec
)

type selfUpdateCapability struct {
	Supported  bool
	Reason     string
	Executable string
}

// agentVersion returns the VCS release tag of this build, or "dev" for a local checkout.
func agentVersion() string {
	if buildVersion != "" {
		return buildVersion
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.modified" && setting.Value == "true" {
				return "dev"
			}
		}
		v := info.Main.Version
		if module.IsPseudoVersion(v) {
			return "dev"
		}
		return v
	}
	return "dev"
}

func probeSelfUpdate(currentVersion, executable string, disabled bool) selfUpdateCapability {
	if disabled {
		return selfUpdateCapability{Supported: false, Reason: "disabled"}
	}
	if currentVersion == "" || currentVersion == "dev" || !semver.IsValid(currentVersion) {
		return selfUpdateCapability{Supported: false, Reason: "dev"}
	}
	resolved, reason := update.WritableExecutable(executable)
	if reason != "" {
		return selfUpdateCapability{Supported: false, Reason: reason, Executable: resolved}
	}
	return selfUpdateCapability{Supported: true, Executable: resolved}
}

func shouldAlignVersion(selfVer, serverVer string, capInfo selfUpdateCapability) bool {
	if !capInfo.Supported {
		return false
	}
	if serverVer == "" || serverVer == "dev" || !semver.IsValid(serverVer) {
		return false
	}
	return serverVer != selfVer
}

func releaseEndpoint(origin, id, file string) (string, error) {
	u, err := url.Parse(origin)
	secure := u != nil && (u.Scheme == "https" || u.Scheme == "http" && u.Hostname() == "127.0.0.1")
	if err != nil || !secure || u.User != nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("the machine address must be an HTTPS origin without a path or query")
	}
	if id == "" || strings.ContainsAny(id, "/\\?#.%") {
		return "", errors.New("invalid environment ID")
	}
	return u.JoinPath("machine", id, "release", file).String(), nil
}

// selfUpdateTo downloads the matching dex-agent archive from the Dex server, verifies its SHA-256
// digest and reported --version, atomically replaces executable, and re-execs in place.
func selfUpdateTo(ctx context.Context, client *http.Client, cfg device, executable, targetVersion string) error {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	archiveName := fmt.Sprintf("dex-agent_%s_%s_%s.tar.gz", targetVersion, runtime.GOOS, runtime.GOARCH)
	checksumURL, err := releaseEndpoint(cfg.Origin, cfg.Environment, "checksums.txt")
	if err != nil {
		return err
	}
	archiveURL, err := releaseEndpoint(cfg.Origin, cfg.Environment, archiveName)
	if err != nil {
		return err
	}
	checksums, err := fetchMachineAsset(ctx, client, checksumURL, cfg.Token, maxRunnerChecksums)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	archiveData, err := fetchMachineAsset(ctx, client, archiveURL, cfg.Token, maxRunnerArchiveBytes)
	if err != nil {
		return fmt.Errorf("download %s: %w", archiveName, err)
	}
	if err := update.VerifyChecksum(checksums, archiveData, archiveName); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(executable), ".dex-agent-update-*")
	if err != nil {
		return fmt.Errorf("executable directory is not writable: %w", err)
	}
	stage := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(stage)
		}
	}()
	if err := update.ExtractBinary(archiveData, "dex-agent", file); err != nil {
		return err
	}
	if err := file.Chmod(0o755); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	gotVer, err := probeAgentBinaryVersion(verifyCtx, stage)
	if err != nil {
		return fmt.Errorf("staged dex-agent did not run: %w", err)
	}
	if gotVer != targetVersion {
		return fmt.Errorf("staged dex-agent reported %q, want %q", gotVer, targetVersion)
	}
	if err := update.ReplaceExecutable(executable, stage); err != nil {
		return err
	}
	keep = true
	log.Printf("updated dex-agent to %s; restarting in place", targetVersion)
	return reexecAgent(executable, os.Args, os.Environ())
}

func fetchMachineAsset(ctx context.Context, client *http.Client, target, token string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil) //nolint:gosec // Validated Dex origin from device.json.
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req) //nolint:gosec // Validated Dex origin from device.json.
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("asset exceeds size limit")
	}
	return data, nil
}

func probeAgentBinaryVersion(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, path, "--version").Output() //nolint:gosec // Verifies the newly staged executable before replacement.
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[0] != "dex-agent" {
		return "", fmt.Errorf("unexpected version output: %q", strings.TrimSpace(string(out)))
	}
	return fields[1], nil
}

type updateBackoff struct {
	version string
	until   time.Time
	delay   time.Duration
}

func (b *updateBackoff) blocked(version string, now time.Time) bool {
	return b.version == version && now.Before(b.until)
}

func (b *updateBackoff) recordFailure(version string, now time.Time) {
	if b.version != version || b.delay == 0 {
		b.version = version
		b.delay = time.Minute
	} else {
		b.delay = min(b.delay*2, 5*time.Minute)
	}
	b.until = now.Add(b.delay)
}

func (b *updateBackoff) clear() {
	*b = updateBackoff{}
}
