package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/recally-io/dex/internal/host/update"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

const (
	maxRunnerArchiveBytes = 256 << 20
	maxRunnerChecksums    = 8 << 20
)

var (
	buildVersion      string
	errUpdateRollback = errors.New("could not restore the previous agent")
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

// selfUpdateTo downloads the matching dex-agent archive from the Dex server, verifies its SHA-256
// digest and reported --version, atomically replaces executable, and re-execs in place.
func selfUpdateTo(ctx context.Context, client *http.Client, cfg device, executable, targetVersion string) error {
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	archiveName := fmt.Sprintf("dex-agent_%s_%s_%s.tar.gz", targetVersion, runtime.GOOS, runtime.GOARCH)
	checksumURL, err := endpoint(cfg.Origin, cfg.RunnerID, "release", "checksums.txt")
	if err != nil {
		return err
	}
	archiveURL, err := endpoint(cfg.Origin, cfg.RunnerID, "release", archiveName)
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
	if err := update.VerifyChecksum(checksums, bytes.NewReader(archiveData), archiveName); err != nil {
		return err
	}
	stage, err := update.StageExecutable(ctx, executable, "dex-agent", targetVersion, archiveData)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(stage) }() //nolint:gosec // StageExecutable creates this temporary file.
	if err := update.ReplaceExecutable(executable, stage); err != nil {
		return err
	}
	log.Printf("updated dex-agent to %s; restarting in place", targetVersion)
	if err := update.Reexec(executable, os.Args, os.Environ()); err != nil {
		execErr := fmt.Errorf("re-exec updated dex-agent: %w", err)
		if restoreErr := update.RestorePrevious(executable); restoreErr != nil {
			return errors.Join(errUpdateRollback, execErr, restoreErr)
		}
		return execErr
	}
	return nil
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
