package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/mcptest"
)

func TestUserMCPDisconnectDuringLoadingKeepsFailedStatus(t *testing.T) {
	t.Parallel()
	if role := os.Getenv("DEX_LOADING_MCP_CHILD"); role != "" {
		dir := os.Getenv("MCP_DIR")
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		t.Cleanup(cancel)
		if role == "later" {
			extensionFile(t, filepath.Join(dir, "connecting"), "")
			if err := waitMCPLoadingFile(ctx, filepath.Join(dir, "release")); err != nil {
				t.Fatal(err)
			}
		} else {
			go func() {
				if err := waitMCPLoadingFile(ctx, filepath.Join(dir, "disconnect")); err != nil {
					os.Exit(8)
				}
				// Closing stdout gives the real client EOF while the later server waits.
				_ = os.Stdout.Close()
				if err := os.WriteFile(filepath.Join(dir, "disconnected"), nil, 0600); err != nil {
					os.Exit(9)
				}
				os.Exit(0)
			}()
		}
		if err := (&mcptest.Server{Dir: dir}).ServeStdio(os.Stdin, os.Stdout); err != nil {
			t.Fatal(err)
		}
		cancel()
		os.Exit(0)
	}

	// Given two real stdio servers and later disabled and invalid entries.
	home, data := t.TempDir(), t.TempDir()
	dir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(data, "agents", "a", "workspace"), 0700); err != nil {
		t.Fatal(err)
	}
	store := testMCPStore(t, data)
	for _, role := range []string{"early", "later"} {
		name := "a_early"
		if role == "later" {
			name = "b_later"
		}
		testSetMCP(t, store, "", name, serverConfig{
			Command: binary,
			Args:    []string{"-test.run=^TestUserMCPDisconnectDuringLoadingKeepsFailedStatus$"},
			// The transport waits for process exit; do not delay it under -race.
			Env: map[string]string{"DEX_LOADING_MCP_CHILD": role, "MCP_DIR": dir, "GORACE": "atexit_sleep_ms=0"},
		})
	}
	testSetMCP(t, store, "", "c_disabled", serverConfig{Disabled: true})
	testSetMCP(t, store, "", "d_invalid", serverConfig{Command: filepath.Join(dir, "missing-command")})
	bots := coreEngine(t, Config{DataDir: data, Home: home, MCPServers: store})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	// When the early server disconnects while the later server is still connecting.
	coordinated := make(chan error, 1)
	go func() {
		defer func() {
			// Release the child even if coordination fails; Reload still has its deadline.
			_ = os.WriteFile(filepath.Join(dir, "release"), nil, 0600)
		}()
		if err := waitMCPLoadingFile(ctx, filepath.Join(dir, "connecting")); err != nil {
			coordinated <- err
			return
		}
		if err := os.WriteFile(filepath.Join(dir, "disconnect"), nil, 0600); err != nil {
			coordinated <- err
			return
		}
		coordinated <- waitMCPLoadingFile(ctx, filepath.Join(dir, "disconnected"))
	}()
	err = bots.Reload(ctx, "a")
	if coordinationErr := <-coordinated; coordinationErr != nil {
		t.Fatal(coordinationErr)
	}
	if err != nil {
		t.Fatal(err)
	}

	// Then status queries retain the failure and the later server's tools still work.
	for {
		info, err := bots.Extensions(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		if len(info.Servers) != 4 {
			t.Fatalf("server statuses: %#v", info.Servers)
		}
		if info.Servers[0].State == "failed" {
			for i, want := range []struct{ name, state string }{{"a_early", "failed"}, {"b_later", "connected"}, {"c_disabled", "disabled"}, {"d_invalid", "failed"}} {
				if got := info.Servers[i]; got.Name != want.name || got.State != want.state {
					t.Fatalf("server %d: %#v, want %s %s", i, got, want.name, want.state)
				}
			}
			if !strings.Contains(info.Servers[0].Error, "closed the connection") {
				t.Fatalf("disconnect error: %q", info.Servers[0].Error)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("disconnect status was lost: %#v", info.Servers)
		case <-time.After(5 * time.Millisecond):
		}
	}
	ref := coreCreate(t, bots, "a", coreOptions("tool"))
	outcome := coreWait(t, coreSubmit(t, bots, ref, "append", `mcp__b_later__append {"text":"loaded after disconnect"}`, "tool"))
	if outcome.Status != "done" || !strings.Contains(outcome.Answer, "appended 1") {
		t.Fatalf("later server tool: %+v", outcome)
	}
	artifact := filepath.Join(dir, "notes.txt")
	raw, err := os.ReadFile(artifact)
	if err != nil || string(raw) != "loaded after disconnect\n" {
		t.Fatalf("later server artifact: %q %v", raw, err)
	}
	t.Logf("artifact: %s; content: %q", artifact, raw)
}

func waitMCPLoadingFile(ctx context.Context, path string) error {
	for {
		_, err := os.Stat(path)
		if err == nil {
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for %s: %w (last observation: %w)", path, context.Cause(ctx), err)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
