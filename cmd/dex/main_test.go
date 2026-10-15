package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/vaayne/dex/internal/service"
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
	c, err = configured("serve", []string{"--port", "18788"}, c)
	if err != nil || c.Host != "::1" || c.Port != 18788 {
		t.Fatalf("%+v: %v", c, err)
	}
}
func TestUserReinstallKeepsOmittedOptions(t *testing.T) {
	c := service.Config{Host: "127.0.0.1", Port: 18787, DataDir: t.TempDir(), Channel: "prerelease", AutoUpdate: true}
	got, err := configured("service install", []string{"--port", "18788"}, c)
	c.Port = 18788
	if err != nil || got != c {
		t.Fatalf("omitted options changed: %+v: %v", got, err)
	}
}
