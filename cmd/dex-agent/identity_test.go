package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	t.Setenv("USER", "vaayne")
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
	state := deviceState(t, home, `{"origin":"https://dex.example.com","environmentId":"01JRUN","token":"machine-token-that-must-stay-private","mode":"user","account":"vaayne","uid":501,"home":"`+home+`"}`)
	var out bytes.Buffer
	if err := status(t.Context(), state, &out); err != nil {
		t.Fatalf("status of an installed machine: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"mode: the account that installed the machine, vaayne (uid 501, home " + home + ")",
		"state: " + state,
		"dex: https://dex.example.com (environment 01JRUN)",
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
		if !strings.Contains(out.String(), "account: vaayne") {
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
		if err == nil || !strings.Contains(err.Error(), "is not readable by vaayne") {
			t.Fatalf("status of a state that this account may not read: %v", err)
		}
		if !strings.Contains(err.Error(), "install it again for vaayne") {
			t.Fatalf("the refusal does not say what to do: %v", err)
		}
	})
}

// TestUserKeepsADedicatedAccountOutOfTheModeOfAPerson checks that a state that an earlier agent
// wrote stays a dedicated account, so the mode of a person is never inferred.
func TestUserKeepsADedicatedAccountOutOfTheModeOfAPerson(t *testing.T) {
	home := fixtureAccount(t)
	state := deviceState(t, home, `{"origin":"https://dex.example.com","environmentId":"01JRUN","token":"`+strings.Repeat("t", 32)+`"}`)
	var out bytes.Buffer
	if err := status(t.Context(), state, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "mode: dedicated account") {
		t.Fatalf("an older state is not a dedicated account:\n%s", out.String())
	}
}

// TestUserEnrollsTheMachineWithTheModeThatTheInstallerChose checks the request that the install
// command makes: only a machine that runs as its install user reports that account.
func TestUserEnrollsTheMachineWithTheModeThatTheInstallerChose(t *testing.T) {
	home := fixtureAccount(t)
	var reported []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RunAsUser string `json:"runAsUser"`
		}
		if err := json.UnmarshalRead(io.LimitReader(r.Body, 4096), &body); err != nil {
			t.Errorf("pair body: %v", err)
		}
		reported = append(reported, body.RunAsUser)
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
			want := ""
			if mode == modeUser {
				want = currentAccount().Name
			}
			if got := reported[len(reported)-1]; got != want {
				t.Fatalf("mode %s reported the account %q, want %q", mode, got, want)
			}
			var stored device
			raw, err := os.ReadFile(filepath.Join(state, "device.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &stored); err != nil {
				t.Fatal(err)
			}
			if stored.Mode != mode || stored.runAsUser() != want {
				t.Fatalf("state records mode %q and account %q, want %q", stored.Mode, stored.runAsUser(), want)
			}
		})
	}
	t.Run("unknown mode", func(t *testing.T) {
		before := len(reported)
		err := enroll(t.Context(), filepath.Join(home, "state-unknown"), srv.URL, "01JRUN", "one-time-code", "root")
		if err == nil || !strings.Contains(err.Error(), "unknown mode") {
			t.Fatalf("enroll with an unknown mode: %v", err)
		}
		if len(reported) != before {
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
