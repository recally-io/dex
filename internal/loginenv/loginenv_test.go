package loginenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeShell writes a shell that runs rc, then the -c command, like `zsh -l -i -c`.
func fakeShell(t *testing.T, rc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shell")
	script := "#!/bin/sh\n" + rc + "\nshift 3\nexec /bin/sh -c \"$1\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUserToolsSeeTheLoginShellEnvironment(t *testing.T) {
	// Given a login shell whose rc file prints a greeting and replaces PATH with a custom directory.
	t.Setenv("SHELL", fakeShell(t, "echo 'welcome back'\nexport FROM_RC='rc value' PATH=/home/test/bin"))
	// When dex captures the environment from a small process environment.
	env, err := captureShellEnv(t.Context(), []string{"PATH=/usr/bin:/bin", "HOME=/home/test"}, 5*time.Second, false)
	// Then the export and the inherited values are present, and the greeting is not.
	if err != nil || env["FROM_RC"] != "rc value" || env["HOME"] != "/home/test" || env["PATH"] != "/home/test/bin" {
		t.Fatalf("environment: %v %v", env, err)
	}
	for key, value := range env {
		if strings.Contains(key+value, "welcome") {
			t.Fatalf("rc output leaked into %s", key)
		}
	}
}

func TestUserHungLoginShellDoesNotBlockTools(t *testing.T) {
	// Given an rc file that starts a background job and never returns.
	t.Setenv("SHELL", fakeShell(t, "sleep 60 &\nsleep 60"))
	started := time.Now()
	// When dex captures the environment with a short limit.
	_, err := captureShellEnv(t.Context(), []string{"PATH=/usr/bin:/bin"}, 300*time.Millisecond, false)
	// Then the capture fails soon, also with the background job alive.
	if err == nil || time.Since(started) > 3*time.Second {
		t.Fatalf("capture: %v after %s", err, time.Since(started))
	}
}

func TestUserRcBackgroundJobDoesNotLoseTheEnvironment(t *testing.T) {
	// Given an rc file that starts a job which keeps stdout open after the shell exits.
	t.Setenv("SHELL", fakeShell(t, "export FROM_RC=kept\n(sleep 2) &"))
	// When dex captures the environment.
	env, err := captureShellEnv(t.Context(), []string{"PATH=/usr/bin:/bin"}, 5*time.Second, false)
	// Then the complete output still counts.
	if err != nil || env["FROM_RC"] != "kept" {
		t.Fatalf("environment: %v %v", env["FROM_RC"], err)
	}
}

func TestUserDexSettingsDoNotReachTheLoginShell(t *testing.T) {
	// Given dex started with its API token in the process environment.
	t.Setenv("DEX_TOKEN", "private-dex-token")
	t.Setenv("SHELL", fakeShell(t, "export FROM_RC=1"))
	// When dex captures the login shell from the tool base keys.
	env, err := Environment(t.Context(), map[string]string{"PATH": "/usr/bin:/bin", "HOME": t.TempDir()})
	// Then tools get the shell exports and not the dex token.
	if err != nil || env["FROM_RC"] != "1" || env["DEX_TOKEN"] != "" {
		t.Fatalf("environment: FROM_RC=%q DEX_TOKEN set=%v err=%v", env["FROM_RC"], env["DEX_TOKEN"] != "", err)
	}
}

func TestUserLoginShellWithoutShellVariable(t *testing.T) {
	// Given a service manager that does not set SHELL.
	t.Setenv("SHELL", "")
	// When dex looks up the login shell, then it uses the shell of the user account.
	shell := loginShell(t.Context())
	if info, err := os.Stat(shell); err != nil || !filepath.IsAbs(shell) || info.IsDir() {
		t.Fatalf("shell %q: %v", shell, err)
	}
	t.Logf("account shell: %s", shell)
}

func TestUserLoginPathProbeEndsOnTimeout(t *testing.T) {
	// Given a shell that does not exit, when the probe hits its deadline,
	// then it returns no PATH and a timeout category promptly.
	t.Setenv("SHELL", fakeShell(t, "exec /bin/sleep 60"))
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	path, err := Path(ctx)
	if path != "" || err == nil || err.Error() != "probe timed out" || time.Since(started) > 3*time.Second {
		t.Fatalf("expected no PATH and a timeout, got a PATH: %t, error: %v, elapsed: %s", path != "", err, time.Since(started))
	}
}

func TestUserLoginPathProbeRejectsOversizedProfileOutput(t *testing.T) {
	// Given a shell that produces a large greeting, when the probe captures PATH,
	// then it fails with a fixed category instead of retaining or printing the greeting.
	t.Setenv("SHELL", fakeShell(t, "printf '%70000s' synthetic-profile-data"))
	t.Setenv("PATH", "/usr/bin:/bin")
	path, err := Path(t.Context())
	if path != "" || err == nil || err.Error() != "profile output limit" {
		t.Fatalf("expected no PATH and an output-limit reason, got a PATH: %t, error: %v", path != "", err)
	}
}
