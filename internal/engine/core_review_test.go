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
	"github.com/recally-io/dex/internal/mcptest"
)

func TestUserCanStopOpenConversationDuringMaintenance(t *testing.T) {
	t.Parallel()
	// Given an idle open conversation whose engine holds the maintenance gate.
	bots := coreEngine(t, Config{})
	ref := coreCreate(t, bots, "alpha", coreOptions("echo"))
	if err := bots.Quiesce(t.Context()); err != nil {
		t.Fatal(err)
	}
	// When the caller requests cancellation without admitting new work.
	if err := bots.StopSession(t.Context(), ref); err != nil {
		t.Fatalf("maintenance blocked Stop: %v", err)
	}
	// Then cancellation completes and admission remains paused.
	if _, err := bots.Submit(t.Context(), ref, Input{RequestID: "blocked", Options: coreOptions("echo")}); !errors.Is(err, ErrMaintenance) {
		t.Fatalf("Stop released maintenance: %v", err)
	}
	bots.Resume()
	if outcome := coreWait(t, coreSubmit(t, bots, ref, "after", "after stop", "echo")); outcome.Status != "done" {
		t.Fatalf("after Stop: %+v", outcome)
	}
	if err := bots.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := bots.StopSession(t.Context(), ref); !errors.Is(err, ErrClosed) {
		t.Fatalf("Stop after close: %v", err)
	}
}

func TestRecoveryReferencesRemainUntilBotOpens(t *testing.T) {
	t.Parallel()
	bots := coreEngine(t, Config{})
	refs := []Ref{{BotID: "alpha", ID: 1}, {BotID: "beta", ID: 2}}
	if err := bots.Recover(t.Context(), refs); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bots.config.DataDir, "agents", "alpha", "state.sqlite")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := bots.Create(t.Context(), "alpha", coreOptions("echo")); err == nil {
		t.Fatal("opening a directory as SQLite must fail")
	}
	if len(bots.recovering["alpha"]) != 1 {
		t.Fatal("failed open discarded recovery references")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	coreCreate(t, bots, "alpha", coreOptions("echo"))
	if len(bots.recovering["alpha"]) != 0 || len(bots.recovering["beta"]) != 1 {
		t.Fatal("successful open must clear only its own recovery references")
	}
}

func TestUserStopDuringMaintenanceDoesNotResumeUnopenedExecution(t *testing.T) {
	t.Parallel()
	// Given an unfinished tool whose next start would create another external action.
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	first := coreEngine(t, config)
	ref := coreCreate(t, first, "alpha", coreOptions("bash"))
	workspace := filepath.Join(config.DataDir, "agents", "alpha", "workspace")
	coreSubmit(t, first, ref, "running", "echo start >> starts; touch started; while :; do sleep 1; done", "bash")
	coreFile(t, filepath.Join(workspace, "started"))
	if err := first.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	// When maintenance starts before the stored Harness opens and Stop is requested.
	reopened := coreEngine(t, config)
	if err := reopened.Quiesce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := reopened.StopSession(t.Context(), ref); !errors.Is(err, ErrMaintenance) {
		t.Fatalf("unopened Stop must defer native cancellation: %v", err)
	}
	// Then Stop has not opened or resumed the persisted execution.
	actions, err := os.ReadFile(filepath.Join(workspace, "starts"))
	if err != nil {
		t.Fatal(err)
	}
	if string(actions) != "start\n" {
		t.Fatalf("Stop resumed execution: %q", actions)
	}
	if len(reopened.resources) != 0 {
		t.Fatal("Stop opened a Harness during maintenance")
	}
	if !reopened.recovering[ref.BotID][durable.ConversationId(ref.ID)] {
		t.Fatal("Stop must isolate stored work before opening can resume")
	}
	reopened.Resume()
	if err := reopened.StopSession(t.Context(), ref); err != nil {
		t.Fatalf("Stop after maintenance: %v", err)
	}
	if err := reopened.CheckIdle(t.Context(), ref); err != nil {
		t.Fatalf("Stop left native work running: %v", err)
	}
	actions, err = os.ReadFile(filepath.Join(workspace, "starts"))
	if err != nil {
		t.Fatal(err)
	}
	if string(actions) != "start\n" {
		t.Fatalf("Stop retry resumed execution: %q", actions)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := reopened.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestUserReloadStartsOnlyOneReplacementMCP(t *testing.T) {
	t.Parallel()
	if os.Getenv("DEX_CORE_RELOAD_CHILD") == "1" {
		file, err := os.OpenFile(os.Getenv("MCP_STARTS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(7)
		}
		_, err = file.WriteString("start\n")
		err = errors.Join(err, file.Close())
		if err != nil {
			os.Exit(8)
		}
		if err := (&mcptest.Server{Dir: os.Getenv("MCP_DIR")}).ServeStdio(os.Stdin, os.Stdout); err != nil {
			os.Exit(9)
		}
		os.Exit(0)
	}
	// Given two open bots with real MCP child processes.
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	starts := filepath.Join(t.TempDir(), "mcp-starts")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config.MCPServers = testMCPStore(t, config.DataDir)
	testSetMCP(t, config.MCPServers, "", "notes", serverConfig{Command: binary, Args: []string{"-test.run=^TestUserReloadStartsOnlyOneReplacementMCP$"}, Env: map[string]string{"DEX_CORE_RELOAD_CHILD": "1", "MCP_STARTS": starts, "MCP_DIR": t.TempDir()}})
	bots := coreEngine(t, config)
	alpha := coreCreate(t, bots, "alpha", coreOptions("echo"))
	coreCreate(t, bots, "beta", coreOptions("echo"))
	// When only alpha reloads, validation must not create a disposable connection.
	if err := bots.Reload(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	// Then beta stays open and alpha starts exactly one replacement child.
	data, err := os.ReadFile(starts)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "start\nstart\nstart\n" {
		t.Fatalf("MCP starts after selected-bot reload: %q", data)
	}
	// Invalid target configuration must be rejected before any child starts or closes.
	extensionFile(t, filepath.Join(config.DataDir, "vault", "agent-alpha.age"), `invalid line`)
	if err := bots.Reload(t.Context(), "alpha"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid reload: %v", err)
	}
	after, err := os.ReadFile(starts)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(data) {
		t.Fatalf("invalid reload started another MCP: %q", after)
	}
	if outcome := coreWait(t, coreSubmit(t, bots, alpha, "after-invalid", `mcp__notes__append {"text":"still connected"}`, "tool")); outcome.Status != "done" || !strings.Contains(outcome.Answer, "appended 1") {
		t.Fatalf("invalid reload lost loaded MCP: %+v", outcome)
	}
	t.Logf("artifact: %s", starts)
}
