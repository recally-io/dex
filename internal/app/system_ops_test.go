package app_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/service"
	"github.com/recally-io/dex/internal/sysops"
)

func buildDexArchive(t *testing.T, tag string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	script := "#!/bin/sh\nprintf 'dex " + tag + "\\n'\n"
	if err := tw.WriteHeader(&tar.Header{
		Name:     "dex",
		Typeflag: tar.TypeReg,
		Mode:     0o755,
		Size:     int64(len(script)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tw, script); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type fixtureReleaseSpec struct {
	tag        string
	prerelease bool
	notes      string
}

func startReleaseFixture(t *testing.T, releases []fixtureReleaseSpec) *httptest.Server {
	t.Helper()
	archives := map[string][]byte{}
	checksums := map[string]string{}
	for _, r := range releases {
		data := buildDexArchive(t, r.tag)
		name := fmt.Sprintf("dex_%s_%s_%s.tar.gz", r.tag, runtime.GOOS, runtime.GOARCH)
		sum := sha256.Sum256(data)
		archives[r.tag] = data
		checksums[r.tag] = hex.EncodeToString(sum[:]) + "  " + name + "\n"
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasPrefix(req.URL.Path, "/asset/"):
			tag := strings.TrimPrefix(req.URL.Path, "/asset/")
			_, _ = w.Write(archives[tag])
		case strings.HasPrefix(req.URL.Path, "/checksums/"):
			tag := strings.TrimPrefix(req.URL.Path, "/checksums/")
			_, _ = io.WriteString(w, checksums[tag])
		case strings.Contains(req.URL.Path, "/releases/tags/"):
			tag := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
			for _, r := range releases {
				if r.tag == tag {
					_ = json.MarshalWrite(w, map[string]any{
						"tag_name":     r.tag,
						"prerelease":   r.prerelease,
						"published_at": "2026-11-23T04:00:00Z",
						"body":         r.notes,
						"html_url":     srv.URL + "/releases/" + r.tag,
						"assets": []map[string]string{
							{"name": fmt.Sprintf("dex_%s_%s_%s.tar.gz", r.tag, runtime.GOOS, runtime.GOARCH), "url": srv.URL + "/asset/" + r.tag},
							{"name": "checksums.txt", "url": srv.URL + "/checksums/" + r.tag},
						},
					})
					return
				}
			}
			http.NotFound(w, req)
		default:
			items := make([]map[string]any, 0, len(releases))
			for _, r := range releases {
				items = append(items, map[string]any{
					"tag_name":     r.tag,
					"prerelease":   r.prerelease,
					"published_at": "2026-11-23T04:00:00Z",
					"body":         r.notes,
					"html_url":     srv.URL + "/releases/" + r.tag,
					"assets": []map[string]string{
						{"name": fmt.Sprintf("dex_%s_%s_%s.tar.gz", r.tag, runtime.GOOS, runtime.GOARCH), "url": srv.URL + "/asset/" + r.tag},
						{"name": "checksums.txt", "url": srv.URL + "/checksums/" + r.tag},
					},
				})
			}
			_ = json.MarshalWrite(w, items)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestUserChecksAndSwitchesUpdateChannel(t *testing.T) {
	t.Parallel()
	// Given a release build pointed at a local public release server with stable and rc releases.
	relSrv := startReleaseFixture(t, []fixtureReleaseSpec{
		{tag: "v1.0.0", prerelease: false, notes: "Stable release"},
		{tag: "v1.1.0-rc.1", prerelease: true, notes: "Candidate release"},
	})
	exeDir := t.TempDir()
	exe := filepath.Join(exeDir, "dex")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf 'dex v1.0.0\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	d := startDexWith(t, app.Config{
		DataDir:        dataDir,
		Version:        "v1.0.0",
		Executable:     exe,
		ReleaseBase:    relSrv.URL,
		RequestRestart: func(string) {},
	})

	// When the user checks updates on the default stable channel, no newer stable version exists.
	var status api.SystemStatus
	d.must(t, http.MethodPost, "/api/system/check-update", nil, &status)
	if status.UpdateChannel != api.Stable || status.UpdateAvailable || status.Latest == nil || status.Latest.Tag != "v1.0.0" {
		t.Fatalf("stable check = %+v", status)
	}

	// When the user switches to prerelease and checks again, v1.1.0-rc.1 is available and persisted.
	d.must(t, http.MethodPut, "/api/system/settings", map[string]string{"updateChannel": "prerelease"}, &status)
	if status.UpdateChannel != api.Prerelease {
		t.Fatalf("expected prerelease channel, got %q", status.UpdateChannel)
	}
	d.must(t, http.MethodPost, "/api/system/check-update", nil, &status)
	if !status.UpdateAvailable || status.Latest == nil || status.Latest.Tag != "v1.1.0-rc.1" {
		t.Fatalf("prerelease check = %+v", status)
	}

	// Then restarting dex preserves the chosen update channel in control.sqlite.
	d.stop()
	d2 := startDexWith(t, app.Config{DataDir: dataDir, Version: "v1.0.0", Executable: exe, ReleaseBase: relSrv.URL})
	d2.must(t, http.MethodGet, "/api/system/status", nil, &status)
	if status.UpdateChannel != api.Prerelease {
		t.Fatalf("persisted channel = %q, want prerelease", status.UpdateChannel)
	}
}

func TestUserBacksUpDatabaseAndReadsRecentLogs(t *testing.T) {
	t.Parallel()
	// Given a running dex server with an attached log ring buffer.
	logBuf := sysops.NewLogBuffer(100)
	logger := slog.New(sysops.TeeHandler(slog.DiscardHandler, logBuf))
	logger.Info("system ops test marker", "scope", "ops")

	d := startDexWith(t, app.Config{
		DataDir: t.TempDir(),
		Version: "v1.0.0",
		Logs:    logBuf,
	})

	// When the user triggers a manual database backup and reads recent logs.
	var backup api.SystemBackup
	d.must(t, http.MethodPost, "/api/system/backup", nil, &backup)
	if backup.SizeBytes <= 0 || !strings.HasPrefix(backup.Name, "control-v1.0.0-") {
		t.Fatalf("unexpected backup: %+v", backup)
	}

	var status api.SystemStatus
	d.must(t, http.MethodGet, "/api/system/status", nil, &status)
	if len(status.Backups) != 1 || status.Backups[0].Name != backup.Name {
		t.Fatalf("status backups = %+v", status.Backups)
	}

	var logs api.SystemLogs
	d.must(t, http.MethodGet, "/api/system/logs?limit=50", nil, &logs)
	found := false
	for _, entry := range logs.Entries {
		if entry.Message == "system ops test marker" && entry.Attrs != nil && strings.Contains(*entry.Attrs, "scope=ops") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("log marker not found in %+v", logs.Entries)
	}
}

func TestUserUpgradesServerWithAutomaticBackupAndSafeWaitWhenBusy(t *testing.T) {
	t.Parallel()
	// Given a running release build with an active bash tool blocking maintenance.
	relSrv := startReleaseFixture(t, []fixtureReleaseSpec{
		{tag: "v2.0.0", prerelease: false, notes: "New major release"},
	})
	exeDir := t.TempDir()
	exe := filepath.Join(exeDir, "dex")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf 'dex v1.0.0\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	restarted := make(chan string, 1)
	d := startDexWith(t, app.Config{
		DataDir:            t.TempDir(),
		Version:            "v1.0.0",
		Executable:         exe,
		ReleaseBase:        relSrv.URL,
		SystemWaitInterval: 25 * time.Millisecond,
		SystemRestartDelay: 10 * time.Millisecond,
		RequestRestart: func(execPath string) {
			select {
			case restarted <- execPath:
			default:
			}
		},
	})
	d.setModel(t, "bash")
	gateDir := t.TempDir()
	started := filepath.Join(gateDir, "started")
	release := filepath.Join(gateDir, "release")
	id := d.newChat(t, fmt.Sprintf("touch '%s'; while [ ! -f '%s' ]; do sleep 0.02; done; echo done", started, release))
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	eventually(t, "tool running", func() (bool, any) { _, err := os.Stat(started); return err == nil, err })

	// When the user triggers a non-forced upgrade while the tool is running.
	var status api.SystemStatus
	d.must(t, http.MethodPost, "/api/system/upgrade", map[string]any{"version": "v2.0.0"}, &status)

	// Then dex stages v2.0.0, creates a backup, and waits for the active tool instead of interrupting it.
	eventually(t, "waiting_idle with backup", func() (bool, any) {
		d.must(t, http.MethodGet, "/api/system/status", nil, &status)
		return status.State == api.SystemStatusStateWaitingIdle && len(status.Backups) == 1, status
	})
	// The executable is not replaced yet while waiting.
	if v, err := service.BinaryVersion(t.Context(), exe); err != nil || v != "v1.0.0" {
		t.Fatalf("binary replaced while busy: %q %v", v, err)
	}

	// When the tool finishes, dex replaces the binary, preserves .previous, and requests restart.
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	d.watch(t, id).until(t, "tool finishes", answered("bash: done"))

	select {
	case execPath := <-restarted:
		if execPath != exe {
			t.Fatalf("restart execPath = %q, want %q", execPath, exe)
		}
	case <-time.After(10 * time.Second):
		d.must(t, http.MethodGet, "/api/system/status", nil, &status)
		t.Fatalf("timed out waiting for upgrade restart; status=%+v", status)
	}

	if v, err := service.BinaryVersion(t.Context(), exe); err != nil || v != "v2.0.0" {
		t.Fatalf("upgraded binary version = %q, err=%v", v, err)
	}
	if prev, err := os.ReadFile(exe + ".previous"); err != nil || !strings.Contains(string(prev), "v1.0.0") {
		t.Fatalf("previous binary missing or wrong: %q %v", prev, err)
	}
}

func TestUserRestartsServerWhenIdleAndWaitsWhenBusy(t *testing.T) {
	t.Parallel()
	// Given a server with an active bash tool.
	var restartCount atomic.Int32
	d := startDexWith(t, app.Config{
		DataDir:            t.TempDir(),
		Version:            "v1.0.0",
		SystemWaitInterval: 25 * time.Millisecond,
		SystemRestartDelay: 10 * time.Millisecond,
		RequestRestart:     func(string) { restartCount.Add(1) },
	})
	d.setModel(t, "bash")
	gateDir := t.TempDir()
	started := filepath.Join(gateDir, "started")
	release := filepath.Join(gateDir, "release")
	d.newChat(t, fmt.Sprintf("touch '%s'; while [ ! -f '%s' ]; do sleep 0.02; done; echo done", started, release))
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	eventually(t, "tool running", func() (bool, any) { _, err := os.Stat(started); return err == nil, err })

	// When the user requests a graceful restart while busy, it waits instead of cutting off the run.
	var status api.SystemStatus
	d.must(t, http.MethodPost, "/api/system/restart", map[string]bool{"force": false}, &status)
	if status.State != api.SystemStatusStateWaitingIdle {
		t.Fatalf("expected waiting_idle, got %+v", status)
	}

	// When the user cancels the pending restart, state returns to idle and no restart runs.
	d.must(t, http.MethodPost, "/api/system/cancel", nil, &status)
	if status.State != api.SystemStatusStateIdle || restartCount.Load() != 0 {
		t.Fatalf("cancel failed: status=%+v restarts=%d", status, restartCount.Load())
	}

	// When the user forces a restart, restart triggers immediately.
	d.must(t, http.MethodPost, "/api/system/restart", map[string]bool{"force": true}, &status)
	if status.State != api.SystemStatusStateRestarting {
		t.Fatalf("expected restarting, got %+v", status)
	}
	eventually(t, "restart callback fired", func() (bool, any) {
		return restartCount.Load() == 1, restartCount.Load()
	})
}

func TestUserCannotUpgradeDevBuild(t *testing.T) {
	t.Parallel()
	// Given a dev build.
	d := startDexWith(t, app.Config{
		DataDir:        t.TempDir(),
		Version:        "dev",
		RequestRestart: func(string) {},
	})

	// When the user inspects status and attempts an upgrade.
	var status api.SystemStatus
	d.must(t, http.MethodGet, "/api/system/status", nil, &status)
	if status.Capability.Supported || status.Capability.Reason == nil || *status.Capability.Reason != "dev" {
		t.Fatalf("unexpected dev capability: %+v", status.Capability)
	}
	d.must(t, http.MethodPost, "/api/system/upgrade", nil, nil, http.StatusConflict)
}
