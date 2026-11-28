package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
	"github.com/recally-io/dex/localdex"
)

func TestUserOnPhoneCannotSaveAnHTTPAddress(t *testing.T) {
	t.Parallel()
	if nsgotest.Target() != "" {
		t.Skip("This test uses a headless iPhone environment. The iOS smoke test uses native input.")
	}
	app := nsgotest.Start(t, root, nsgotest.Environment(core.Environment{OS: core.OSIOS}))
	app.Find("role:button Connect")
	if app.Exists("role:button Retry") {
		t.Fatal("first launch on a phone must not try a default server")
	}

	app.Type("id:server-address", "http://127.0.0.1:8787")
	app.Press("role:button Connect")
	app.Find("text:Enter a server address with no sign-in details")
	if app.Exists("id:dex-workspace") {
		t.Fatal("a phone must not open a cleartext origin")
	}
	if _, ok := app.Storage().GetItem(savedOriginKey); ok {
		t.Fatal("a phone must not save a cleartext origin")
	}
}

func TestUserChoosesAndForgetsLocalWorkspace(t *testing.T) {
	t.Parallel()
	if nsgotest.Target() != "" {
		t.Skip("This test uses an isolated headless iPhone environment.")
	}
	var dir string
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		dir = cx.DataDir()
		return root(cx)
	}, nsgotest.Environment(core.Environment{OS: core.OSIOS}))
	app.Find("role:button Connect")
	if dir == "" {
		t.Fatal("test did not provide an app data directory")
	}
	// A workspace already in use must not be opened by this app.
	config := localdex.Config{DataDir: filepath.Join(dir, "dex"), Version: "test", Mobile: true}
	inUse, err := localdex.Start(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inUse.Close() })
	app.Press("role:button Open Local Workspace")
	app.WaitFor("text:Could not start the local workspace: workspace is already open in Dex", 15*time.Second)
	if err := inUse.Close(); err != nil {
		t.Fatal(err)
	}
	app.Press("role:button Retry")
	app.WaitFor("id:dex-workspace", 15*time.Second)
	if got, _ := app.Storage().GetItem(savedOriginKey); got != localWorkspaceTarget {
		t.Fatalf("saved workspace = %q, want local", got)
	}

	app.OpenLinks(workspaceSettingsURL)
	app.Press("role:button Back to workspace")
	app.Find("id:dex-workspace")
	app.OpenLinks(workspaceSettingsURL)
	app.Press("role:button Forget local selection")
	app.WaitUntil(15*time.Second, func() bool {
		_, saved := app.Storage().GetItem(savedOriginKey)
		return !saved
	})
	reopened, err := localdex.Start(t.Context(), config)
	if err != nil {
		t.Fatalf("reopen %s after forgetting: %v", config.DataDir, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUserLocalWorkspaceDataDir(t *testing.T) {
	appDir := t.TempDir()
	override := t.TempDir()
	appDexDir := filepath.Join(appDir, "dex")
	for _, tc := range []struct {
		name     string
		appDir   string
		mac      bool
		override string
		want     string
	}{
		{"macOS default", appDir, true, "", appDexDir},
		{"macOS override", "", true, override, override},
		{"mobile default", appDir, false, "", appDexDir},
		{"mobile ignores override", appDir, false, override, appDexDir},
		{"macOS missing app directory", "", true, "", ""},
		{"mobile missing app directory", "", false, override, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DEX_DATA_DIR", tc.override)
			got, err := localDataDir(tc.appDir, tc.mac)
			if tc.want == "" {
				if err == nil || err.Error() != "app data directory is not available" {
					t.Fatalf("expected a missing app data directory error, got %q, %v", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("local data directory = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestUserEntersServerOrigin(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		input string
		mac   bool
		want  string
	}{
		{" HTTPS://DEX.Example.COM:443/ ", false, "https://dex.example.com"},
		{"https://Dex.Example.com:8443", false, "https://dex.example.com:8443"},
		{"https://[2001:db8:0::1]/", false, "https://[2001:db8::1]"},
		{"http://127.0.0.1:8787/", true, "http://127.0.0.1:8787"},
		{"http://127.0.0.1:8787", false, ""},
		{"http://localhost:8787", true, ""},
		{"http://127.0.0.2:8787", true, ""},
		{"http://127.0.0.1.example.com:8787", true, ""},
		{"https://user:secret@dex.example.com", false, ""},
		{"https://dex.example.com/login", false, ""},
		{"https://dex.example.com/%2f", false, ""},
		{"https://dex.example.com?", false, ""},
		{"https://dex.example.com#", false, ""},
		{"https://dex.example.com:", false, ""},
		{"https://dex.example.com:0", false, ""},
		{"https://dex.example.com:65536", false, ""},
		{"https://0x7f.1", false, ""},
		{"https://[dex.example.com]", false, ""},
		{"https://[fe80::1%25en0]", false, ""},
		{"https://dex.example.com\\.invalid", false, ""},
		{"dex.example.com", false, ""},
	} {
		got, ok := canonicalOrigin(tc.input, tc.mac)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("canonicalOrigin(%q, %v) = %q, %v; want %q", tc.input, tc.mac, got, ok, tc.want)
		}
	}
}

func TestUserConnectionProbeNeverFollowsRedirect(t *testing.T) {
	t.Parallel()
	var arrivals atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		arrivals.Add(1)
	}))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	if err := probeDex(t.Context(), server.URL, true, nil); err == nil {
		t.Fatal("connection succeeded through a redirect")
	}
	if n := arrivals.Load(); n != 0 {
		t.Fatalf("redirect destination received %d requests", n)
	}
}

func TestUserConnectionProbeRejectsUntrustedTLS(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("HTTP request reached the server without a trusted certificate")
	}))
	defer server.Close()

	if err := probeDex(t.Context(), server.URL, false, nil); err == nil {
		t.Fatal("connection accepted an untrusted TLS certificate")
	}
}

func TestUserConnectionProbeRequiresRemoteAuth(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		mac     bool
		status  int
		wantErr error
	}{
		{"protected remote", false, http.StatusUnauthorized, nil},
		{"unauthenticated remote", false, http.StatusOK, errDexAuthRequired},
		{"protected Mac loopback", true, http.StatusUnauthorized, nil},
		{"unauthenticated Mac loopback", true, http.StatusOK, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.RequestURI() != "/api/health" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
					t.Error("expected an unauthenticated GET /api/health")
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			if err := probeDex(t.Context(), server.URL, tc.mac, server.Client().Transport); !errors.Is(err, tc.wantErr) {
				t.Fatalf("connection check: %v; want %v", err, tc.wantErr)
			}
		})
	}
}
