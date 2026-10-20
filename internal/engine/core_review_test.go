package engine

import (
	"context"
	"encoding/json/v2"
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
	// Given an idle open conversation whose engine holds the maintenance gate.
	agents := coreEngine(t, Config{})
	ref := coreCreate(t, agents, "alpha", coreOptions("echo"))
	if err := agents.Quiesce(t.Context()); err != nil {
		t.Fatal(err)
	}
	// When the caller requests cancellation without admitting new work.
	if err := agents.Abort(t.Context(), ref); err != nil {
		t.Fatalf("maintenance blocked Stop: %v", err)
	}
	// Then cancellation completes and admission remains paused.
	if _, err := agents.Submit(t.Context(), ref, Input{RequestID: "blocked", Options: coreOptions("echo")}); !errors.Is(err, ErrMaintenance) {
		t.Fatalf("Stop released maintenance: %v", err)
	}
	agents.Resume()
	if outcome := coreWait(t, coreSubmit(t, agents, ref, "after", "after stop", "echo")); outcome.Status != "done" {
		t.Fatalf("after Stop: %+v", outcome)
	}
	if err := agents.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := agents.Abort(t.Context(), ref); !errors.Is(err, ErrClosed) {
		t.Fatalf("Stop after close: %v", err)
	}
}

func TestRecoveryReferencesRemainUntilAgentOpens(t *testing.T) {
	agents := coreEngine(t, Config{})
	refs := []Ref{{AgentID: "alpha", ID: 1}, {AgentID: "beta", ID: 2}}
	if err := agents.Recover(t.Context(), refs); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(agents.config.DataDir, "agents", "alpha", "state.sqlite")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := agents.Create(t.Context(), "alpha", coreOptions("echo")); err == nil {
		t.Fatal("opening a directory as SQLite must fail")
	}
	if len(agents.recovering["alpha"]) != 1 {
		t.Fatal("failed open discarded recovery references")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	coreCreate(t, agents, "alpha", coreOptions("echo"))
	if len(agents.recovering["alpha"]) != 0 || len(agents.recovering["beta"]) != 1 {
		t.Fatal("successful open must clear only its own recovery references")
	}
}

func TestUserStopDuringMaintenanceDoesNotResumeUnopenedExecution(t *testing.T) {
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
	if err := reopened.Abort(t.Context(), ref); !errors.Is(err, ErrMaintenance) {
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
	if !reopened.recovering[ref.AgentID][durable.ConversationId(ref.ID)] {
		t.Fatal("Stop must isolate stored work before opening can resume")
	}
	reopened.Resume()
	if err := reopened.Abort(t.Context(), ref); err != nil {
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
	// Given two open agents with real MCP child processes.
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	starts := filepath.Join(t.TempDir(), "mcp-starts")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	server := map[string]any{"mcpServers": map[string]any{"notes": map[string]any{"command": binary, "args": []string{"-test.run=^TestUserReloadStartsOnlyOneReplacementMCP$"}, "env": map[string]string{"DEX_CORE_RELOAD_CHILD": "1", "MCP_STARTS": starts, "MCP_DIR": t.TempDir()}}}}
	raw, err := json.Marshal(server)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config.DataDir, ".agents", "mcp.json")
	extensionFile(t, path, string(raw))
	agents := coreEngine(t, config)
	alpha := coreCreate(t, agents, "alpha", coreOptions("echo"))
	coreCreate(t, agents, "beta", coreOptions("echo"))
	// When only alpha reloads, validation must not create a disposable connection.
	if err := agents.Reload(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	// Then beta stays open and alpha starts exactly one replacement child.
	data, err := os.ReadFile(starts)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "start\nstart\nstart\n" {
		t.Fatalf("MCP starts after selected-agent reload: %q", data)
	}
	// Invalid target configuration must be rejected before any child starts or closes.
	extensionFile(t, path, `{"mcpServers":false}`)
	if err := agents.Reload(t.Context(), "alpha"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid reload: %v", err)
	}
	after, err := os.ReadFile(starts)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(data) {
		t.Fatalf("invalid reload started another MCP: %q", after)
	}
	if outcome := coreWait(t, coreSubmit(t, agents, alpha, "after-invalid", `mcp__notes__append {"text":"still connected"}`, "tool")); outcome.Status != "done" || !strings.Contains(outcome.Answer, "appended 1") {
		t.Fatalf("invalid reload lost loaded MCP: %+v", outcome)
	}
	t.Logf("artifact: %s", starts)
}
