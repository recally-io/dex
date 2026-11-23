package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func pickFreePort(t *testing.T) int {
	t.Helper()
	l, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // tcp listener always uses TCPAddr
	_ = l.Close()
	return port
}

func fetchHealth(ctx context.Context, base string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/health", nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("health status %d", resp.StatusCode)
	}
	var body struct {
		StartedAt int64 `json:"startedAt"`
	}
	if err := json.UnmarshalRead(resp.Body, &body); err != nil {
		return 0, err
	}
	return body.StartedAt, nil
}

func TestUserServeLogsReexecsInPlaceAndExitsOnSIGTERM(t *testing.T) {
	// Given a real compiled dex binary started with `dex serve` in an isolated data directory.
	bin := filepath.Join(t.TempDir(), "dex")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build dex: %v\n%s", err, out)
	}
	port := pickFreePort(t)
	dataDir := t.TempDir()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	var stderr lockedBuffer
	cmd := exec.CommandContext(t.Context(), bin, "serve", "--host", "127.0.0.1", "--port", fmt.Sprintf("%d", port), "--data-dir", dataDir) //nolint:gosec // test binary built above
	cmd.Env = append(os.Environ(), "DEX_FAKE_MODEL=1", "HOME="+t.TempDir())
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGKILL)
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	})
	// Then dex emits "dex is running" to stderr without deadlocking and serves /api/health.
	var firstStarted int64
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stderr.String(), "dex is running") {
			if started, err := fetchHealth(t.Context(), base); err == nil && started > 0 {
				firstStarted = started
				break
			}
		}
		select {
		case err := <-done:
			t.Fatalf("dex exited early: %v\nstderr:\n%s", err, stderr.String())
		case <-time.After(50 * time.Millisecond):
		}
	}
	if firstStarted == 0 {
		t.Fatalf("timed out waiting for dex serve startup; stderr:\n%s", stderr.String())
	}

	// When the user requests a restart, the process stays alive and serves a new startedAt.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/api/system/restart", strings.NewReader(`{"force":false}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restart status = %d", resp.StatusCode)
	}

	var secondStarted int64
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if started, err := fetchHealth(t.Context(), base); err == nil && started > firstStarted {
			secondStarted = started
			break
		}
		select {
		case err := <-done:
			t.Fatalf("dex exited instead of re-execing: %v\nstderr:\n%s", err, stderr.String())
		case <-time.After(50 * time.Millisecond):
		}
	}
	if secondStarted <= firstStarted {
		t.Fatalf("startedAt did not advance after restart (%d -> %d)\nstderr:\n%s", firstStarted, secondStarted, stderr.String())
	}
	// When SIGTERM is sent, the re-execed server exits promptly instead of hanging.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dex exited with error on SIGTERM: %v\nstderr:\n%s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("dex hung on SIGTERM\nstderr:\n%s", stderr.String())
	}
}
