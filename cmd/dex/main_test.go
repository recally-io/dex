package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/service"
)

func TestUserRequestsUsage(t *testing.T) {
	if os.Getenv("DEX_CLI_TEST") == "1" {
		os.Args = []string{"dex", os.Getenv("DEX_CLI_ARG")}
		main()
		os.Exit(0)
	}
	// Given an isolated CLI process, with no access to the user's dex data.
	for _, arg := range []string{"help", "unknown"} {
		t.Run(arg, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestUserRequestsUsage$")
			dir := t.TempDir()
			cmd.Env = append(os.Environ(), "DEX_CLI_TEST=1", "DEX_CLI_ARG="+arg, "HOME="+dir, "XDG_CACHE_HOME="+dir, "DEX_DATA_DIR="+dir, "DEX_ADDR=127.0.0.1:0")
			// When the user asks for help or uses an unknown subcommand.
			b, err := cmd.CombinedOutput()
			// Then help succeeds and an unknown command exits with status 2.
			exit, ok := errors.AsType[*exec.ExitError](err)
			validExit := arg == "help" && err == nil || arg == "unknown" && ok && exit.ExitCode() == 2
			if !validExit || !strings.Contains(string(b), "Usage: dex") {
				t.Fatalf("want help or usage error; got %v: %s", err, b)
			}
		})
	}
}

func TestUserFlagsOverrideEnvironmentParts(t *testing.T) {
	t.Setenv("DEX_ADDR", "[::1]:18787")
	t.Setenv("DEX_DATA_DIR", t.TempDir())
	c, err := service.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	err = configured(flags("serve"), []string{"--port", "18788"}, &c)
	if err != nil || c.Host != "::1" || c.Port != 18788 {
		t.Fatalf("%+v: %v", c, err)
	}
}

func TestUserLeadingFlagsSelectServe(t *testing.T) {
	// Given foreground flags without an explicit serve command.
	// When serve rejects a weak DEX_TOKEN, the error must come from serve configuration.
	t.Setenv("DEX_TOKEN", "short")
	err := dispatch([]string{"--host", "0.0.0.0", "--port", "18788", "--data-dir", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "DEX_TOKEN") {
		t.Fatalf("leading flags did not select serve: %v", err)
	}
}

func TestUserTokenSelection(t *testing.T) {
	loopback, network := service.Config{Host: "127.0.0.1"}, service.Config{Host: "0.0.0.0"}
	own := "user-token-0123456789abcdefghijklmnopqrstuvwxyz"
	for _, tc := range []struct {
		name       string
		env        *string
		c          service.Config
		storedOnly bool
		token      string
		require    bool
	}{
		{"loopback without DEX_TOKEN", nil, loopback, false, "", false},
		{"network host uses the stored token", nil, network, false, "", true},
		{"DEX_TOKEN turns auth on for loopback", &own, loopback, false, own, true},
		{"the service ignores DEX_TOKEN", &own, network, true, "", true},
	} {
		if tc.env == nil {
			t.Setenv("DEX_TOKEN", "")
			os.Unsetenv("DEX_TOKEN") //nolint:errcheck // t.Setenv restores it
		} else {
			t.Setenv("DEX_TOKEN", *tc.env)
		}
		token, require, err := serveAuth(tc.c, tc.storedOnly)
		if err != nil || token != tc.token || require != tc.require {
			t.Fatalf("%s: token=%q require=%t err=%v", tc.name, token, require, err)
		}
	}
	// A set but empty DEX_TOKEN is a mistake, not "no token".
	t.Setenv("DEX_TOKEN", "")
	if _, _, err := serveAuth(network, false); err == nil {
		t.Fatal("an empty DEX_TOKEN was accepted")
	}
}

func TestUserStatusWithoutAService(t *testing.T) {
	// Given no installed service, when the user asks for its status, then dex reports it.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, args := range [][]string{{"service", "status"}, {"service", "status", "--json"}} {
		if err := dispatch(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestUserReinstallKeepsStoredOptions(t *testing.T) {
	// Given an installed definition with non-default data and update channel.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m, err := service.New()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../internal/service/testdata/" + runtime.GOOS + "-false.golden")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(m.Definition()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.Definition(), data, 0600); err != nil {
		t.Fatal(err)
	}
	stored, err := m.Read()
	if err != nil {
		t.Fatal(err)
	}
	// When reinstall supplies only the port, retain boot options but replace the old PATH.
	t.Setenv("PATH", "/transient/install-shell/bin")
	got, err := parseInstallConfig(m, []string{"--port", "18788"})
	stored.Port = 18788
	stored.Path = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	if runtime.GOOS == "darwin" {
		stored.Path = "/opt/homebrew/bin:" + stored.Path
	}
	if err != nil || got != stored {
		t.Fatalf("reinstall lost stored options: %+v: %v", got, err)
	}
}

func TestUserDailyUpdatesKeepOnlyTheGitHubCLIDirectory(t *testing.T) {
	// Given the real gh executable exposed through an install-shell PATH with unrelated tools.
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Skip("gh is not installed")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DEX_ADDR", "")
	t.Setenv("DEX_DATA_DIR", "")
	dir := t.TempDir()
	if err := os.Symlink(gh, filepath.Join(dir, "gh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/transient/unrelated-tools")
	m, err := service.New()
	if err != nil {
		t.Fatal(err)
	}

	// When daily updates are enabled, retain gh discovery but not the install-shell PATH.
	c, err := parseInstallConfig(m, []string{"--auto-update"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.Path, dir+":") || strings.Contains(c.Path, "/transient/") {
		t.Fatalf("daily updates have the wrong service PATH: %s", c.Path)
	}

	// When daily updates are disabled, the service no longer needs the gh directory.
	c, err = parseInstallConfig(m, []string{"--auto-update=false"})
	if err != nil || strings.Contains(c.Path, dir) {
		t.Fatalf("disabled updates retained the gh directory: %s: %v", c.Path, err)
	}

	// When gh is unavailable, reject daily updates before changing the installed service.
	t.Setenv("PATH", t.TempDir())
	if _, err := parseInstallConfig(m, []string{"--auto-update"}); err == nil {
		t.Fatal("daily updates accepted a PATH without gh")
	}
}
