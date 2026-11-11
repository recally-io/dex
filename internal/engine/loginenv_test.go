package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/durable"
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
	// Given a login shell whose rc file prints a greeting and exports a value.
	shell := fakeShell(t, "echo 'welcome back'\nexport FROM_RC='rc value'")
	// When dex captures the environment from a small process environment.
	env, err := captureShellEnv(t.Context(), shell, []string{"PATH=/usr/bin:/bin", "HOME=/home/test"}, 5*time.Second)
	// Then the export and the inherited values are present, and the greeting is not.
	if err != nil || env["FROM_RC"] != "rc value" || env["HOME"] != "/home/test" {
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
	shell := fakeShell(t, "sleep 60 &\nsleep 60")
	started := time.Now()
	// When dex captures the environment with a short limit.
	_, err := captureShellEnv(t.Context(), shell, []string{"PATH=/usr/bin:/bin"}, 300*time.Millisecond)
	// Then the capture fails soon, also with the background job alive.
	if err == nil || time.Since(started) > 3*time.Second {
		t.Fatalf("capture: %v after %s", err, time.Since(started))
	}
}

func TestUserSeesAWarningWhenTheLoginShellFails(t *testing.T) {
	// Given a capture that fails.
	home, data := t.TempDir(), t.TempDir()
	env := newToolEnv(home, map[string]string{"PATH": "/usr/bin:/bin"}, func(context.Context, map[string]string) (map[string]string, error) {
		return nil, errors.New("exit status 1")
	})
	a := newAgentExtensions(home, data, "a", env)
	loaded, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close(t.Context())
	// Then tools get the fallback, and the Extensions page explains why.
	if loaded.Env["PATH"] != "/usr/bin:/bin" {
		t.Fatalf("environment: %v", loaded.Env)
	}
	info, _ := a.Info(t.Context())
	if !strings.Contains(strings.Join(info.Warnings, "\n"), "login shell environment is not available") {
		t.Fatalf("warnings: %v", info.Warnings)
	}
}

func TestUserLoginShellIsCapturedAgainAfterAnRcEdit(t *testing.T) {
	// Given a loaded agent with a captured environment.
	home, data := t.TempDir(), t.TempDir()
	value := "first"
	env := newToolEnv(home, nil, func(context.Context, map[string]string) (map[string]string, error) {
		return map[string]string{"FROM_RC": value}, nil
	})
	a := newAgentExtensions(home, data, "a", env)
	loaded, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close(t.Context())
	// When the user edits .zshrc.
	value = "second"
	extensionFile(t, filepath.Join(home, ".zshrc"), "export FROM_RC=second\n")
	// Then the agent is stale, and the next load sees the new value.
	if a.stamp() == loaded.stamp {
		t.Fatal("rc edit did not make the agent stale")
	}
	next, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close(t.Context())
	if next.Env["FROM_RC"] != "second" {
		t.Fatalf("environment: %v", next.Env)
	}
}

func TestUserBotUsesOnePasswordThroughTheCLI(t *testing.T) {
	// Given an op executable in PATH, and shell credentials of the other mode.
	home, data, bin := t.TempDir(), t.TempDir(), t.TempDir()
	extensionFile(t, filepath.Join(bin, "op"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(bin, "op"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := newToolEnv(home, map[string]string{"PATH": bin, "OP_CONNECT_HOST": "http://shell", "OP_CONNECT_TOKEN": "shell-token"}, nil)
	a := newAgentExtensions(home, data, "a", env)
	before := a.stamp()
	// When the user selects a service account.
	env.setOnePassword(OnePassword{Mode: "service-account", ServiceAccountToken: "ops-token"})
	if a.stamp() == before {
		t.Fatal("1Password change did not make the agent stale")
	}
	loaded, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close(t.Context())
	// Then tools get only the service account credentials, and the prompt explains op read.
	if loaded.Env["OP_SERVICE_ACCOUNT_TOKEN"] != "ops-token" || loaded.Env["OP_CONNECT_HOST"] != "" || loaded.Env["OP_CONNECT_TOKEN"] != "" {
		t.Fatalf("environment: %v", loaded.Env)
	}
	guide, err := loaded.Extension.Sections[0].Render(t.Context(), durable.PromptInput{})
	if err != nil || guide == nil || !strings.Contains(*guide, "op read") || strings.Contains(*guide, "ops-token") {
		t.Fatalf("guide: %v %v", guide, err)
	}
	// When op is not installed, then the prompt does not mention it and the page warns.
	env = newToolEnv(home, map[string]string{"PATH": t.TempDir()}, nil)
	env.setOnePassword(OnePassword{Mode: "connect", ConnectHost: "http://connect", ConnectToken: "c-token"})
	a = newAgentExtensions(home, data, "a", env)
	missing, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Close(t.Context())
	guide, _ = missing.Extension.Sections[0].Render(t.Context(), durable.PromptInput{})
	info, _ := a.Info(t.Context())
	if strings.Contains(*guide, "op read") || !strings.Contains(strings.Join(info.Warnings, "\n"), "op CLI is not in the PATH") {
		t.Fatalf("missing op: %s %v", *guide, info.Warnings)
	}
}

func TestUserRcBackgroundJobDoesNotLoseTheEnvironment(t *testing.T) {
	// Given an rc file that starts a job which keeps stdout open after the shell exits.
	shell := fakeShell(t, "export FROM_RC=kept\n(sleep 30) &")
	// When dex captures the environment.
	env, err := captureShellEnv(t.Context(), shell, []string{"PATH=/usr/bin:/bin"}, 5*time.Second)
	// Then the complete output still counts.
	if err != nil || env["FROM_RC"] != "kept" {
		t.Fatalf("environment: %v %v", env["FROM_RC"], err)
	}
}

func TestUserBrokenShellIsNotRetriedOnEveryOpen(t *testing.T) {
	// Given a login shell that fails.
	home := t.TempDir()
	calls := 0
	env := newToolEnv(home, map[string]string{"PATH": "/usr/bin"}, func(context.Context, map[string]string) (map[string]string, error) {
		calls++
		return nil, errors.New("exit status 1")
	})
	// When two agents open, then dex runs the shell once.
	env.load(t.Context())
	env.load(t.Context())
	if calls != 1 {
		t.Fatalf("captures after two opens: %d", calls)
	}
	// When the user edits an rc file, then dex tries again.
	extensionFile(t, filepath.Join(home, ".zshrc"), "export FIXED=1\n")
	env.load(t.Context())
	if calls != 2 {
		t.Fatalf("captures after an rc edit: %d", calls)
	}
}

func TestUserFailedRecaptureKeepsTheLastEnvironment(t *testing.T) {
	// Given tools that have the captured login shell PATH.
	home := t.TempDir()
	var fail error
	env := newToolEnv(home, map[string]string{"PATH": "/usr/bin"}, func(context.Context, map[string]string) (map[string]string, error) {
		if fail != nil {
			return nil, fail
		}
		return map[string]string{"PATH": "/opt/homebrew/bin:/usr/bin"}, nil
	})
	env.load(t.Context())
	// When an rc edit makes the next capture time out.
	fail = errors.New("login shell did not finish in 10s")
	extensionFile(t, filepath.Join(home, ".zshrc"), "sleep 20\n")
	got, _ := env.load(t.Context())
	// Then tools keep the last PATH, and the warning says why.
	_, _, warning := env.snapshot()
	if got["PATH"] != "/opt/homebrew/bin:/usr/bin" || !strings.Contains(warning, "keep the last captured environment") {
		t.Fatalf("environment after a failed capture: %v %q", got, warning)
	}
}

func TestUserDexSettingsDoNotReachTheLoginShell(t *testing.T) {
	// Given dex started with its API token in the process environment.
	t.Setenv("DEX_TOKEN", "private-dex-token")
	t.Setenv("SHELL", fakeShell(t, "export FROM_RC=1"))
	// When dex captures the login shell from the tool base keys.
	env, err := LoginShellEnv(t.Context(), map[string]string{"PATH": "/usr/bin:/bin", "HOME": t.TempDir()})
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
