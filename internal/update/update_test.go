package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/service"
)

type archiveEntry struct {
	name    string
	kind    byte
	size    int64
	content string
}

func archive(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		size := int64(len(entry.content))
		if entry.size != 0 {
			size = entry.size
		}
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Typeflag: entry.kind, Mode: 0755, Size: size, Linkname: "elsewhere"}); err != nil {
			t.Fatal(err)
		}
		if entry.content != "" {
			if _, err := io.WriteString(tw, entry.content); err != nil {
				t.Fatal(err)
			}
		}
	}
	// An oversized fixture intentionally contains only a header.
	_ = tw.Close()
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func fixtureBinary(tag string) string { return "#!/bin/sh\nprintf 'dex " + tag + "\\n'\n" }

func fixtureUpdater(t *testing.T, data []byte, tag, mode string) (*Updater, string, service.Manager) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	path := filepath.Join(dir, "dex")
	if err := os.WriteFile(path, []byte(fixtureBinary("v1.0.0")), 0755); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("dex_%s_%s_%s.tar.gz", tag, runtime.GOOS, runtime.GOARCH)
	digest := sha256.Sum256(data)
	checksum := hex.EncodeToString(digest[:]) + "  " + name + "\n"
	if mode == "checksum" {
		checksum = strings.Repeat("0", 64) + "  " + name + "\n"
	}
	var redirect *httptest.Server
	if mode == "redirect" {
		redirect = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				t.Error("authorization header crossed hosts")
			}
			_, _ = w.Write(data)
		}))
		t.Cleanup(redirect.Close)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("release API did not receive its token")
		}
		switch r.URL.Path {
		case "/asset":
			if r.Header.Get("Accept") != "application/octet-stream" {
				t.Error("asset API needs octet-stream")
			}
			if mode == "redirect" {
				http.Redirect(w, r, redirect.URL, http.StatusFound)
				return
			}
			if mode == "interrupted" {
				w.Header().Set("Content-Length", strconv.Itoa(len(data)+100))
			}
			_, _ = w.Write(data)
		case "/checksums":
			_, _ = io.WriteString(w, checksum)
		default:
			if mode == "no-stable" {
				_ = json.MarshalWrite(w, []release{{Tag: tag, Prerelease: true}})
				return
			}
			assets := []asset{{Name: name, URL: server.URL + "/asset"}, {Name: "checksums.txt", URL: server.URL + "/checksums"}}
			if mode == "missing" {
				assets = assets[1:]
			}
			value := release{Tag: tag, Assets: assets}
			if strings.Contains(r.URL.Path, "/tags/") {
				_ = json.MarshalWrite(w, value)
			} else {
				_ = json.MarshalWrite(w, []release{value})
			}
		}
	}))
	t.Cleanup(server.Close)
	u, err := New("v1.0.0", path)
	if err != nil {
		t.Fatal(err)
	}
	u.base = server.URL
	u.token = "fixture-token"
	m, err := service.New()
	if err != nil {
		t.Fatal(err)
	}
	return u, path, m
}

func TestUserUpdatesFromVerifiedRelease(t *testing.T) {
	// Given real archives, real checksum downloads, and a standalone executable.
	data := archive(t, archiveEntry{name: "dex", kind: tar.TypeReg, content: fixtureBinary("v2.0.0")}, archiveEntry{name: "README.md", kind: tar.TypeReg, content: "dex"})
	u, path, m := fixtureUpdater(t, data, "v2.0.0", "")
	// When the updater validates, stages, and replaces the executable.
	var output bytes.Buffer
	if err := u.Run(t.Context(), Options{Channel: "stable"}, m, &output); err != nil {
		t.Fatal(err)
	}
	// Then the new executable runs and the old executable remains available.
	b, err := exec.CommandContext(t.Context(), path, "version").Output()
	if err != nil || !strings.Contains(string(b), "v2.0.0") {
		t.Fatalf("new binary: %s: %v", b, err)
	}
	previous, err := os.ReadFile(path + ".previous")
	if err != nil || !strings.Contains(string(previous), "v1.0.0") {
		t.Fatalf("previous binary: %s: %v", previous, err)
	}
	if !strings.Contains(output.String(), "Restart any foreground") {
		t.Fatal(output.String())
	}
}
func TestUserUpdateRejectsUnsafeAssetsBeforeReplacement(t *testing.T) {
	valid := archive(t, archiveEntry{name: "dex", kind: tar.TypeReg, content: fixtureBinary("v2.0.0")})
	cases := []struct {
		name, mode, want string
		data             []byte
	}{
		{"checksum", "checksum", "checksum mismatch", valid},
		{"missing", "missing", "missing release asset", valid},
		{"interrupted", "interrupted", "interrupted", valid},
		{"link", "", "not a regular file", archive(t, archiveEntry{name: "dex", kind: tar.TypeSymlink})},
		{"traversal", "", "no regular file named dex", archive(t, archiveEntry{name: "../dex", kind: tar.TypeReg, content: "bad"})},
		{"duplicate", "", "duplicate", archive(t, archiveEntry{name: "dex", kind: tar.TypeReg, content: "one"}, archiveEntry{name: "dex", kind: tar.TypeReg, content: "two"})},
		{"oversized", "", "size limit", archive(t, archiveEntry{name: "dex", kind: tar.TypeReg, size: maxAsset + 1})},
		{"bad-version", "", "unexpected version", archive(t, archiveEntry{name: "dex", kind: tar.TypeReg, content: fixtureBinary("v3.0.0")})},
		{"no-stable", "no-stable", "no stable release available", valid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, path, m := fixtureUpdater(t, tc.data, "v2.0.0", tc.mode)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			err = u.Run(t.Context(), Options{Channel: "stable"}, m, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q; got %v", tc.want, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("failed update changed the executable: %v", err)
			}
			leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".dex-update-*"))
			if err != nil || len(leftovers) != 0 {
				t.Fatalf("staging was not cleaned: %v %v", leftovers, err)
			}
		})
	}
}
func TestUserUpdateRefusesDowngradeOrSameVersion(t *testing.T) {
	for _, tag := range []string{"v0.9.0", "v1.0.0"} {
		t.Run(tag, func(t *testing.T) {
			u, path, m := fixtureUpdater(t, nil, tag, "")
			var b bytes.Buffer
			if err := u.Run(t.Context(), Options{Channel: "stable"}, m, &b); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(b.String(), "up to date") {
				t.Fatal(b.String())
			}
			if _, err := os.Stat(path + ".previous"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unchanged version made a backup")
			}
		})
	}
}
func TestUserUpdateLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.lock")
	unlock, err := lockUpdate(path)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := lockUpdate(path); !errors.Is(err, ErrBusy) {
		if other != nil {
			other()
		}
		t.Fatalf("concurrent update was not refused: %v", err)
	}
	unlock()
	unlock, err = lockUpdate(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}
func TestUserTokenDoesNotFollowCrossHostRedirect(t *testing.T) {
	// Given an authenticated release whose asset redirects to another host.
	data := archive(t, archiveEntry{name: "dex", kind: tar.TypeReg, content: fixtureBinary("v2.0.0")})
	u, path, m := fixtureUpdater(t, data, "v2.0.0", "redirect")
	// When the full update downloads the asset, its token must remain at the release API.
	if err := u.Run(t.Context(), Options{Channel: "stable"}, m, io.Discard); err != nil {
		t.Fatal(err)
	}
	if version, err := service.BinaryVersion(t.Context(), path); err != nil || version != "v2.0.0" {
		t.Fatalf("redirected update: %s: %v", version, err)
	}
}

func TestUserCheckDoesNotReplaceExecutable(t *testing.T) {
	u, path, m := fixtureUpdater(t, nil, "v2.0.0", "")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := u.Run(t.Context(), Options{Channel: "stable", Check: true}, m, &output); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("check replaced the executable")
	}
	if !strings.Contains(output.String(), "Update available") {
		t.Fatal(output.String())
	}
}
func TestUserAutomaticUpdatesRejectShellOnlyCredentials(t *testing.T) {
	t.Setenv("GH_TOKEN", "shell-only")
	t.Setenv("GITHUB_TOKEN", "also-shell-only")
	t.Setenv("PATH", t.TempDir())
	if _, _, err := auth(t.Context(), true); err == nil {
		t.Fatal("automatic updates accepted shell-only credentials")
	}
	token, source, err := auth(t.Context(), false)
	if err != nil || token != "shell-only" || source != "GH_TOKEN" {
		t.Fatal("manual token precedence changed")
	}
}
func TestUserPrereleaseChannelIncludesCandidates(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.MarshalWrite(w, []release{{Tag: "v1.0.0"}, {Tag: "v1.1.0-rc.1", Prerelease: true}, {Tag: "v2.0.0", Draft: true}})
	}))
	defer fixture.Close()
	u := Updater{base: fixture.URL, client: releaseClient(time.Second)}
	for channel, want := range map[string]string{"stable": "v1.0.0", "prerelease": "v1.1.0-rc.1"} {
		got, err := u.selectRelease(t.Context(), Options{Channel: channel})
		if err != nil || got.Tag != want {
			t.Fatalf("%s: %+v: %v", channel, got, err)
		}
	}
}
func TestUserCannotUpdateDevOrPackageManagedBuild(t *testing.T) {
	if _, err := New("dev", "unused"); err == nil {
		t.Fatal("dev build accepted")
	}
	if _, err := New("v1.0.0", "/usr/bin/env"); err == nil || !strings.Contains(err.Error(), "package-managed") {
		t.Fatalf("system executable accepted: %v", err)
	}
}

func TestUserRequiresCompleteReleaseVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dex")
	if err := os.WriteFile(path, []byte(fixtureBinary("v1.0.0")), 0755); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"v1", "v1.2", "v1.2+metadata"} {
		if _, err := New(tag, path); err == nil {
			t.Fatalf("accepted shortened release version %q", tag)
		}
	}
}

func TestUserUpdatesExactRelease(t *testing.T) {
	// Given a release tag, select its endpoint instead of the channel list.
	data := archive(t, archiveEntry{name: "dex", kind: tar.TypeReg, content: fixtureBinary("v2.0.0")})
	u, path, m := fixtureUpdater(t, data, "v2.0.0", "")
	if err := u.Run(t.Context(), Options{Tag: "v2.0.0"}, m, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got, err := service.BinaryVersion(t.Context(), path); err != nil || got != "v2.0.0" {
		t.Fatalf("exact release: %s: %v", got, err)
	}
}
