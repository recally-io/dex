package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/durable"
)

func TestUserSeesAWarningWhenTheLoginShellFails(t *testing.T) {
	t.Parallel()
	// Given a capture that fails.
	home, data := t.TempDir(), t.TempDir()
	env := newToolEnv(home, map[string]string{"PATH": "/usr/bin:/bin"}, func(context.Context, map[string]string) (map[string]string, error) {
		return nil, errors.New("exit status 1")
	})
	a := newBotExtensions(home, data, "a", nil, env)
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
	t.Parallel()
	// Given a loaded bot with a captured environment.
	home, data := t.TempDir(), t.TempDir()
	value := "first"
	env := newToolEnv(home, nil, func(context.Context, map[string]string) (map[string]string, error) {
		return map[string]string{"FROM_RC": value}, nil
	})
	a := newBotExtensions(home, data, "a", nil, env)
	loaded, err := a.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close(t.Context())
	// When the user edits .zshrc.
	value = "second"
	extensionFile(t, filepath.Join(home, ".zshrc"), "export FROM_RC=second\n")
	// Then the bot is stale, and the next load sees the new value.
	if a.stamp() == loaded.stamp {
		t.Fatal("rc edit did not make the bot stale")
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
	t.Parallel()
	// Given an op executable in PATH, and shell credentials of the other mode.
	home, data, bin := t.TempDir(), t.TempDir(), t.TempDir()
	extensionFile(t, filepath.Join(bin, "op"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(bin, "op"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := newToolEnv(home, map[string]string{"PATH": bin, "OP_CONNECT_HOST": "http://shell", "OP_CONNECT_TOKEN": "shell-token"}, nil)
	a := newBotExtensions(home, data, "a", nil, env)
	before := a.stamp()
	// When the user selects a service account.
	env.setOnePassword(OnePassword{Mode: "service-account", ServiceAccountToken: "ops-token"})
	if a.stamp() == before {
		t.Fatal("1Password change did not make the bot stale")
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
	a = newBotExtensions(home, data, "a", nil, env)
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

func TestUserBrokenShellIsNotRetriedOnEveryOpen(t *testing.T) {
	t.Parallel()
	// Given a login shell that fails.
	home := t.TempDir()
	calls := 0
	env := newToolEnv(home, map[string]string{"PATH": "/usr/bin"}, func(context.Context, map[string]string) (map[string]string, error) {
		calls++
		return nil, errors.New("exit status 1")
	})
	// When two bots open, then dex runs the shell once.
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
	t.Parallel()
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
