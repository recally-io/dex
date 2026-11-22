package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/durable/env"
	"github.com/MichaelKinsy/PiG/durable/env/node"
)

func TestUserRunnerBashFindsLoginToolsFromCustomZshConfig(t *testing.T) {
	// Given a service account with Zsh startup files in an isolated ZDOTDIR.
	if _, err := os.Stat("/bin/zsh"); err != nil {
		t.Skip("/bin/zsh is not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("DEX_FIXTURE_SERVICE_ONLY", "service-value")
	t.Setenv("DEX_FIXTURE_RC_ONLY", "")
	bin, zdot, xdg := filepath.Join(home, "bin"), filepath.Join(home, "zsh"), filepath.Join(home, "xdg")
	t.Setenv("ZDOTDIR", zdot)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	for _, dir := range []string{bin, zdot, xdg} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture(t, filepath.Join(xdg, "dex-fixture-marker"), "", 0o600)
	writeFixture(t, filepath.Join(bin, "dex-fixture-cli"), "#!/bin/sh\nprintf 'found'\n", 0o700)
	writeFixture(t, filepath.Join(zdot, ".zprofile"), `export DEX_FIXTURE_LOGIN=1`, 0o600)
	rc := filepath.Join(zdot, ".zshrc")
	writeFixture(t, rc, `
[[ $DEX_FIXTURE_LOGIN = 1 && -z $DEX_FIXTURE_SERVICE_ONLY ]] || exit 42
[[ $XDG_CONFIG_HOME = "$HOME/xdg" && -f $XDG_CONFIG_HOME/dex-fixture-marker ]] || exit 44
printf 'synthetic profile greeting\n'
export PATH="$HOME/bin"
export ZDOTDIR="$HOME/probe-only-zsh" XDG_CONFIG_HOME="$HOME/probe-only-xdg"
export DEX_FIXTURE_RC_ONLY=profile-value
`, 0o600)

	// When the Runner starts and opens a connection, then Bash resolves the login tool.
	executionOpts := runnerExecutionOptions(t.Context(), t.TempDir())
	runBash := func(opts env.ShellExecOptions, command string) {
		t.Helper()
		execution := node.NewNodeExecutionEnv(executionOpts)
		t.Cleanup(func() { _ = execution.Cleanup(context.Background()) })
		var output strings.Builder
		opts.OnOutput = func(_ context.Context, text string, _ env.ShellOutputInfo) { output.WriteString(text) }
		result, err := execution.Exec(t.Context(), command, &opts)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("Runner Bash exit = %d, error = %v, output = %q", result.ExitCode, err, output.String())
		}
	}
	const ordinaryBash = `
if shopt -q login_shell || [[ $- == *i* ]]; then exit 1; fi
[[ -n $BASH_VERSION && -z $DEX_FIXTURE_RC_ONLY && $DEX_FIXTURE_SERVICE_ONLY = service-value ]] || exit 2
if ! command -v dex-fixture-cli >/dev/null; then echo 'login PATH did not resolve fixture CLI'; exit 3; fi
[[ $(dex-fixture-cli) = found ]] || exit 4
[[ $ZDOTDIR = "$HOME/zsh" && $XDG_CONFIG_HOME = "$HOME/xdg" ]]
`
	runBash(env.ShellExecOptions{}, ordinaryBash)

	// Then reconnects use the startup PATH, even after the rc file changes.
	writeFixture(t, rc, `exit 43`, 0o600)
	runBash(env.ShellExecOptions{}, ordinaryBash)

	// An explicit PATH wins, and disabling inheritance removes both service and login PATH.
	runBash(env.ShellExecOptions{Env: map[string]string{"PATH": "/bin"}}, `[[ $PATH = /bin ]] && ! command -v dex-fixture-cli`)
	runBash(env.ShellExecOptions{InheritEnv: new(false)}, `[[ -z $DEX_FIXTURE_SERVICE_ONLY && -z $ZDOTDIR && -z $XDG_CONFIG_HOME ]] && ! command -v dex-fixture-cli`)
	runBash(env.ShellExecOptions{InheritEnv: new(false), Env: map[string]string{"PATH": "/bin"}}, `[[ $PATH = /bin && -z $DEX_FIXTURE_SERVICE_ONLY ]]`)
}

func TestUserRunnerKeepsServicePathWhenLoginIsUnavailable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", home+":/usr/bin:/bin")
	writeFixture(t, filepath.Join(home, "dex-service-cli"), "#!/bin/sh\nexit 0\n", 0o700)

	for _, tc := range []struct {
		name, script, reason string
	}{
		{name: "nologin", reason: "login disabled"},
		{name: "cannot-start", reason: "login shell could not run"},
		{name: "failed-profile", script: "echo synthetic-private-output; echo synthetic-private-error >&2; exit 1", reason: "login shell exited unsuccessfully"},
		{name: "empty-path", script: `export PATH=''; shift 3; exec /bin/sh -c "$1"`, reason: "no exported PATH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given a Runner whose shell is disabled or cannot provide an exported PATH.
			shell := filepath.Join(home, tc.name)
			if tc.script != "" {
				writeFixture(t, shell, "#!/bin/sh\n"+tc.script+"\n", 0o700)
			}
			t.Setenv("SHELL", shell)
			var logs strings.Builder
			originalWriter, originalFlags := log.Writer(), log.Flags()
			log.SetOutput(&logs)
			log.SetFlags(0)
			t.Cleanup(func() { log.SetOutput(originalWriter); log.SetFlags(originalFlags) })

			// When the Runner starts, then a tool can still resolve the service CLI.
			execution := node.NewNodeExecutionEnv(runnerExecutionOptions(t.Context(), t.TempDir()))
			t.Cleanup(func() { _ = execution.Cleanup(context.Background()) })
			result, err := execution.Exec(t.Context(), "dex-service-cli", nil)
			if err != nil || result.ExitCode != 0 {
				t.Fatalf("service CLI: exit = %d, error = %v", result.ExitCode, err)
			}
			// The only diagnostic is the reason category, not a value or profile output.
			want := "login PATH unavailable (" + tc.reason + "); tools keep the service PATH\n"
			if logs.String() != want {
				t.Fatalf("wanted one diagnostic with reason %q and no profile details", tc.reason)
			}
		})
	}
}

func writeFixture(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}
