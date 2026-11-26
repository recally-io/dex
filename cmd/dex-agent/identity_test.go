package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	json "encoding/json/v2"
)

// fixtureAccount creates a home that only the account that runs the test can read, with a fixture
// CLI on the PATH of its login shell. No test reads the real home of the account that runs it.
func fixtureAccount(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(bin, "gh"), "#!/bin/sh\necho 'fixture gh'\n")
	// The login shell of the fixture account adds the fixture CLI to the PATH, as a real profile
	// adds the tools that the account installed.
	writeExecutable(t, filepath.Join(home, "login-shell"), `#!/bin/sh
PATH="$HOME/bin:/usr/bin:/bin"
export PATH
shift 3
exec /bin/sh -c "$1"
`)
	t.Setenv("HOME", home)
	t.Setenv("SHELL", filepath.Join(home, "login-shell"))
	t.Setenv("PATH", "/usr/bin:/bin")
	// A login session names its account in the environment, and the account database names it too.
	t.Setenv("USER", currentAccount().Name)
	return home
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func deviceState(t *testing.T, home, body string) string {
	t.Helper()
	state := filepath.Join(home, ".local", "state", "dex-agent")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "device.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return state
}

// TestUserSeesTheAccountAndTheToolsOfTheMachineThatRunsTheWork states what a machine that runs as
// the account that installed it reports: the account, its home, and the PATH that its work
// inherits, without the token.
func TestUserSeesTheAccountAndTheToolsOfTheMachineThatRunsTheWork(t *testing.T) {
	home := fixtureAccount(t)
	state := deviceState(t, home, `{"origin":"https://dex.example.com","runnerId":"01JRUN","token":"machine-token-that-must-stay-private","mode":"user"}`)
	var out bytes.Buffer
	if err := status(t.Context(), state, &out); err != nil {
		t.Fatalf("status of an installed machine: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		fmt.Sprintf("mode: user account: %s (uid %d, home %s)", currentAccount().Name, os.Geteuid(), home),
		"state: " + state,
		"dex: https://dex.example.com (runner 01JRUN)",
		"workspace: " + filepath.Join(state, "workspace"),
		"path: " + filepath.Join(home, "bin") + ":",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("status does not report %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "machine-token") {
		t.Fatalf("status prints the token:\n%s", text)
	}
}

// TestUserSeesWhyTheMachineCannotReadItsState distinguishes a machine that was never installed
// from a state directory that the account of the service may not read.
func TestUserSeesWhyTheMachineCannotReadItsState(t *testing.T) {
	home := fixtureAccount(t)
	t.Run("never installed", func(t *testing.T) {
		missing := filepath.Join(home, "nowhere")
		var out bytes.Buffer
		err := status(t.Context(), missing, &out)
		if err == nil || !strings.Contains(err.Error(), "install the machine with the command from Dex") {
			t.Fatalf("status of a machine that was never installed: %v", err)
		}
		if strings.Contains(err.Error(), "not readable") {
			t.Fatalf("a missing state reads as a permission problem: %v", err)
		}
	})
	t.Run("state without a device", func(t *testing.T) {
		state := filepath.Join(home, ".local", "state", "dex-agent")
		if err := os.MkdirAll(state, 0o700); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err := status(t.Context(), state, &out)
		if err == nil || !strings.Contains(err.Error(), "install the machine with the command from Dex") {
			t.Fatalf("status of a state directory without a device: %v", err)
		}
		if !strings.Contains(out.String(), "account: "+currentAccount().Name) {
			t.Fatalf("status does not name the account that reads it:\n%s", out.String())
		}
	})
	t.Run("state of another account", func(t *testing.T) {
		state := deviceState(t, home, `{"mode":"user","account":"someone-else","uid":502}`)
		if err := os.Chmod(state, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(state, 0o700) })
		var out bytes.Buffer
		err := status(t.Context(), state, &out)
		if err == nil || !strings.Contains(err.Error(), "is not readable by "+currentAccount().Name) {
			t.Fatalf("status of a state that this account may not read: %v", err)
		}
		if !strings.Contains(err.Error(), "install it again for "+currentAccount().Name) {
			t.Fatalf("the refusal does not say what to do: %v", err)
		}
	})
}

func TestUserSeesAnUnknownInstallMode(t *testing.T) {
	home := fixtureAccount(t)
	state := deviceState(t, home, `{"origin":"https://dex.example.com","runnerId":"01JRUN","token":"`+strings.Repeat("t", 32)+`","mode":"invalid"}`)
	var out bytes.Buffer
	if err := status(t.Context(), state, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "mode: unknown mode") {
		t.Fatalf("status did not report an unknown mode:\n%s", out.String())
	}
}

func TestUserEnrollsTheMachineWithTheModeThatTheInstallerChose(t *testing.T) {
	home := fixtureAccount(t)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) != 0 {
			t.Errorf("pair must not send local account data: len=%d err=%v", len(body), err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"` + strings.Repeat("a", 40) + `"}`))
	}))
	defer srv.Close()
	for _, mode := range []string{modeUser, modeDedicated} {
		t.Run(mode, func(t *testing.T) {
			state := filepath.Join(home, "state-"+mode)
			if err := enroll(t.Context(), state, srv.URL, "01JRUN", "one-time-code", mode); err != nil {
				t.Fatalf("enroll with mode %s: %v", mode, err)
			}
			var stored device
			raw, err := os.ReadFile(filepath.Join(state, "device.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &stored); err != nil {
				t.Fatal(err)
			}
			if stored.RunnerID != "01JRUN" {
				t.Fatalf("state records Runner ID %q, want 01JRUN", stored.RunnerID)
			}
			if stored.Mode != mode {
				t.Fatalf("state records mode %q, want %q", stored.Mode, mode)
			}
		})
	}
	t.Run("unknown mode", func(t *testing.T) {
		before := requests.Load()
		err := enroll(t.Context(), filepath.Join(home, "state-unknown"), srv.URL, "01JRUN", "one-time-code", "root")
		if err == nil || !strings.Contains(err.Error(), "unknown mode") {
			t.Fatalf("enroll with an unknown mode: %v", err)
		}
		if requests.Load() != before {
			t.Fatal("an unknown mode reached Dex")
		}
	})
}

// TestUserNeverTreatsAMissingStateAsAPermissionProblem checks the message that the service logs.
func TestUserNeverTreatsAMissingStateAsAPermissionProblem(t *testing.T) {
	fixtureAccount(t)
	missing := filepath.Join(t.TempDir(), "gone")
	if err := stateError(missing, &fs.PathError{Op: "open", Path: missing, Err: fs.ErrNotExist}); !errors.Is(err, fs.ErrNotExist) && !strings.Contains(err.Error(), "install the machine") {
		t.Fatalf("a missing state: %v", err)
	}
	if err := stateError(missing, fs.ErrPermission); err == nil || !strings.Contains(err.Error(), "not readable") {
		t.Fatalf("a state of another account: %v", err)
	}
}
