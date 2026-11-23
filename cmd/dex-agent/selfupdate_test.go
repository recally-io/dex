package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func buildAgentArchive(t *testing.T, tag string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	script := "#!/bin/sh\nprintf 'dex-agent " + tag + "\\n'\n"
	if err := tw.WriteHeader(&tar.Header{
		Name:     "dex-agent",
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

func TestUserRunnerSelfUpdatesToMatchServerVersion(t *testing.T) {
	// Given a writable dex-agent binary on v1.0.0 and a Dex server serving v2.0.0 assets under machine auth.
	dir := t.TempDir()
	exe := filepath.Join(dir, "dex-agent")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf 'dex-agent v1.0.0\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	archiveData := buildAgentArchive(t, "v2.0.0")
	archiveName := fmt.Sprintf("dex-agent_v2.0.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(archiveData)
	checksums := hex.EncodeToString(sum[:]) + "  " + archiveName + "\n"
	const token = "machine-token-0123456789abcdefghijklmnopqrstuvwxyz"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/machine/env-1/release/checksums.txt":
			_, _ = io.WriteString(w, checksums)
		case "/machine/env-1/release/" + archiveName:
			_, _ = w.Write(archiveData)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var reexeced string
	origReexec := reexecAgent
	reexecAgent = func(executable string, _ []string, _ []string) error {
		reexeced = executable
		return nil
	}
	t.Cleanup(func() { reexecAgent = origReexec })

	// When selfUpdateTo runs for v2.0.0.
	cfg := device{Origin: srv.URL, Environment: "env-1", Token: token}
	if err := selfUpdateTo(t.Context(), srv.Client(), cfg, exe, "v2.0.0"); err != nil {
		t.Fatal(err)
	}

	// Then the executable is replaced with v2.0.0, .previous preserves v1.0.0, and reexecAgent is invoked.
	if reexeced != exe {
		t.Fatalf("reexec target = %q, want %q", reexeced, exe)
	}
	ver, err := probeAgentBinaryVersion(t.Context(), exe)
	if err != nil || ver != "v2.0.0" {
		t.Fatalf("updated binary version = %q, err=%v", ver, err)
	}
	prev, err := os.ReadFile(exe + ".previous")
	if err != nil || !strings.Contains(string(prev), "v1.0.0") {
		t.Fatalf("previous binary missing or wrong: %q %v", prev, err)
	}
}

func TestUserRunnerSelfUpdateRejectsCorruptChecksumWithoutReplacingBinary(t *testing.T) {
	// Given a Dex server returning a mismatched checksum.
	dir := t.TempDir()
	exe := filepath.Join(dir, "dex-agent")
	original := []byte("#!/bin/sh\nprintf 'dex-agent v1.0.0\\n'\n")
	if err := os.WriteFile(exe, original, 0o755); err != nil {
		t.Fatal(err)
	}
	archiveData := buildAgentArchive(t, "v2.0.0")
	archiveName := fmt.Sprintf("dex-agent_v2.0.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/machine/env-1/release/checksums.txt":
			_, _ = io.WriteString(w, strings.Repeat("0", 64)+"  "+archiveName+"\n")
		case "/machine/env-1/release/" + archiveName:
			_, _ = w.Write(archiveData)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := device{Origin: srv.URL, Environment: "env-1", Token: "tok-0123456789abcdefghijklmnopqrstuvwxyz"}
	if err := selfUpdateTo(t.Context(), srv.Client(), cfg, exe, "v2.0.0"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got %v", err)
	}
	after, err := os.ReadFile(exe)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatalf("binary modified after checksum failure: %v", err)
	}
}

func TestUserRunnerSelfUpdateBackoffAndVersionAlignmentPolicy(t *testing.T) {
	// Given probeSelfUpdate and alignment rules.
	dir := t.TempDir()
	exe := filepath.Join(dir, "dex-agent")
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := probeSelfUpdate("v1.0.0", exe, true); got.Supported || got.Reason != "disabled" {
		t.Fatalf("disabled probe = %+v", got)
	}
	if got := probeSelfUpdate("dev", exe, false); got.Supported || got.Reason != "dev" {
		t.Fatalf("dev probe = %+v", got)
	}
	capOK := probeSelfUpdate("v1.0.0", exe, false)
	if !capOK.Supported {
		t.Fatalf("expected writable executable to support self-update: %+v", capOK)
	}
	if shouldAlignVersion("v1.0.0", "v1.0.0", capOK) || shouldAlignVersion("v1.0.0", "dev", capOK) {
		t.Fatal("should not align matching or dev versions")
	}
	if !shouldAlignVersion("v1.0.0", "v1.1.0-rc.1", capOK) || !shouldAlignVersion("v1.1.0-rc.1", "v1.0.0", capOK) {
		t.Fatal("should align to exact differing server release tag")
	}

	var b updateBackoff
	now := time.Now()
	b.recordFailure("v2.0.0", now)
	if !b.blocked("v2.0.0", now.Add(30*time.Second)) {
		t.Fatal("expected failure backoff to block immediate retry")
	}
	if b.blocked("v2.0.1", now.Add(30*time.Second)) {
		t.Fatal("different version should not be blocked by earlier version backoff")
	}
	b.clear()
	if b.blocked("v2.0.0", now.Add(30*time.Second)) {
		t.Fatal("clear did not reset backoff")
	}
}
