package engine

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/durable"
)

func TestUserStopKeepsQueuedMetadataAcrossRecovery(t *testing.T) {
	for _, mode := range []string{"open", "cold_stop", "startup_recovery"} {
		for _, kind := range []string{"message", "task_result"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				// Given an admitted input behind a running real bash tool.
				config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
				agents := coreEngine(t, config)
				ref := coreCreate(t, agents, "alpha", coreOptions("bash"))
				workspace := filepath.Join(config.DataDir, "agents", "alpha", "workspace")
				coreSubmit(t, agents, ref, "running", "echo start >> starts; touch started; while :; do sleep 1; done", "bash")
				coreFile(t, filepath.Join(workspace, "started"))
				input := Input{RequestID: "queued", Content: "saved passive content", Kind: kind, Origin: "telegram:7", Source: "dex:alpha/chat/1", Attachments: []Attachment{{ID: "file", Name: "note.txt", Path: "uploads/file/note.txt", MimeType: "text/plain", Size: 5}}, Options: coreOptions("echo")}
				if kind == "task_result" {
					input.TaskID, input.TaskTitle = "task-1", "Saved report"
				}
				admitted, err := agents.Submit(t.Context(), ref, input)
				if err != nil {
					t.Fatal(err)
				}
				before, err := agents.Watch(t.Context(), ref)
				if err != nil {
					t.Fatal(err)
				}
				queued := false
				for _, item := range before.Snapshot.Items {
					if item.ID == input.RequestID && item.Queued {
						queued = true
					}
				}
				before.Close()
				if !queued {
					t.Fatal("precondition: admitted input was not queued")
				}

				// When Stop runs on an open agent, a cold agent, or a saved startup abort intent.
				if mode != "open" {
					if err := agents.Close(t.Context()); err != nil {
						t.Fatal(err)
					}
					agents = coreEngine(t, config)
				}
				if mode == "startup_recovery" {
					if err := agents.Recover(t.Context(), []Ref{ref}); err != nil {
						t.Fatal(err)
					}
					// Opening must complete isolation even before the business Abort retry.
					watch, err := agents.Watch(t.Context(), ref)
					if err != nil {
						t.Fatal(err)
					}
					assertWithdrawnInput(t, watch.Snapshot.Items, input)
					watch.Close()
				}
				for range 2 {
					if err := agents.Abort(t.Context(), ref); err != nil {
						t.Fatal(err)
					}
				}
				replay, err := agents.Submit(t.Context(), ref, input)
				if err != nil {
					t.Fatal(err)
				}
				if replay.ID != admitted.ID {
					t.Fatalf("request ID admitted twice: %d != %d", replay.ID, admitted.ID)
				}
				if outcome := coreWait(t, replay); outcome.Status != "unanswered" || outcome.Reason != "aborted" {
					t.Fatalf("native settlement: %+v", outcome)
				}

				// Then snapshot and history retain exactly one passive input after another reopen.
				if err := agents.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				agents = coreEngine(t, config)
				if err := agents.Abort(t.Context(), ref); err != nil {
					t.Fatal(err)
				}
				watch, err := agents.Watch(t.Context(), ref)
				if err != nil {
					t.Fatal(err)
				}
				assertWithdrawnInput(t, watch.Snapshot.Items, input)
				watch.Close()
				history, err := agents.History(t.Context(), ref, 1<<60, 100)
				if err != nil {
					t.Fatal(err)
				}
				assertWithdrawnInput(t, history.Items, input)
				conversation, err := agents.conversation(t.Context(), ref)
				if err != nil {
					t.Fatal(err)
				}
				entries, err := conversation.Entries(t.Context(), durable.EntryQuery{}, 100, nil)
				if err != nil {
					t.Fatal(err)
				}
				found := 0
				for _, entry := range entries.Items {
					if entry.Kind != "dex.withdrawn" {
						continue
					}
					found++
					var metadata InputMetadata
					raw, err := json.Marshal(entry.Data)
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(raw, &metadata); err != nil {
						t.Fatal(err)
					}
					expected := InputMetadata{RequestID: input.RequestID, Content: input.Content, Kind: input.Kind, Origin: input.Origin, Source: input.Source, Attachments: input.Attachments, TaskID: input.TaskID, TaskTitle: input.TaskTitle, Reason: "aborted"}
					if !reflect.DeepEqual(metadata, expected) || len(entry.Model) != 0 {
						t.Fatalf("passive metadata: %+v; model=%+v", metadata, entry.Model)
					}
				}
				if found != 1 {
					t.Fatalf("durable withdrawn entries: %d", found)
				}
				actions, err := os.ReadFile(filepath.Join(workspace, "starts"))
				if err != nil {
					t.Fatal(err)
				}
				if string(actions) != "start\n" {
					t.Fatalf("Stop resumed stored tool: %q", actions)
				}
				if outcome := coreWait(t, coreSubmit(t, agents, ref, "after", "after stop", "context")); outcome.Status != "done" || strings.Contains(outcome.Answer, input.Content) {
					t.Fatalf("withdrawn input reached next model: %+v", outcome)
				}
				t.Logf("request=%s native=unanswered/aborted snapshot=1 history=1 passive=1 tool_starts=1", input.RequestID)
			})
		}
	}
}

func assertWithdrawnInput(t *testing.T, items []Item, input Input) {
	t.Helper()
	shown, stopped := 0, 0
	for _, item := range items {
		if item.ID == input.RequestID {
			shown++
			if item.Queued || item.TaskID != input.TaskID || item.Title != input.TaskTitle {
				t.Errorf("withdrawn display metadata: %+v", item)
			}
			if input.Kind == "message" && (item.Text != input.Content || item.Origin != input.Origin || !reflect.DeepEqual(item.Attachments, input.Attachments)) {
				t.Errorf("withdrawn message metadata: %+v", item)
			}
		}
		if item.ID == input.RequestID+":error" && item.Aborted {
			stopped++
		}
	}
	if shown != 1 || stopped != 1 {
		t.Errorf("withdrawn display: input=%d aborted=%d; items=%+v", shown, stopped, items)
	}
}

func TestUserReloadOpensUnopenedAgentAndReportsMCPResults(t *testing.T) {
	// Given an unopened agent with a real MCP server and an unavailable command.
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	starts := filepath.Join(t.TempDir(), "starts")
	servers := map[string]any{"mcpServers": map[string]any{
		"good":   map[string]any{"command": binary, "args": []string{"-test.run=^TestUserReloadStartsOnlyOneReplacementMCP$"}, "env": map[string]string{"DEX_CORE_RELOAD_CHILD": "1", "MCP_STARTS": starts, "MCP_DIR": t.TempDir()}},
		"broken": map[string]any{"command": filepath.Join(t.TempDir(), "missing-command")},
	}}
	raw, err := json.Marshal(servers)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config.DataDir, "agents", "alpha", ".agents", "mcp.json")
	extensionFile(t, path, string(raw))
	agents := coreEngine(t, config)
	// When the caller explicitly reloads before creating a conversation.
	if err := agents.Reload(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
	// Then the result reports actual connection states, with one MCP start.
	info, err := agents.Extensions(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, server := range info.Servers {
		states[server.Name] = server.State
	}
	t.Logf("MCP connection states: %+v", info.Servers)
	if states["good"] != "connected" || states["broken"] != "failed" {
		t.Errorf("unopened Reload did not connect MCP: %v", states)
	}
	data, err := os.ReadFile(starts)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "start\n" {
		t.Fatalf("MCP starts: %q", data)
	}
}

func TestUserConcurrentCloseAndReloadLeavesNoOpenResources(t *testing.T) {
	// Given an idle engine; race shutdown against reload at the public boundary.
	for range 20 {
		agents := coreEngine(t, Config{})
		coreCreate(t, agents, "alpha", coreOptions("echo"))
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		start, reloaded := make(chan struct{}), make(chan error, 1)
		go func() { <-start; reloaded <- agents.Reload(ctx, "alpha") }()
		close(start)
		closeErr := agents.Close(ctx)
		reloadErr := <-reloaded
		cancel()
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		if reloadErr != nil && !errors.Is(reloadErr, ErrClosed) {
			t.Fatal(reloadErr)
		}
		// Then Close owns every installed resource and future Reload reports ErrClosed.
		if err := agents.Reload(t.Context(), "alpha"); !errors.Is(err, ErrClosed) {
			t.Fatalf("Reload after Close: %v", err)
		}
		for _, resource := range agents.resources {
			select {
			case <-resource.harness.Closed():
			default:
				_ = resource.harness.Close(t.Context())
				_ = resource.loaded.Close(t.Context())
				t.Fatal("Close completed but concurrent Reload installed a live Harness")
			}
		}
	}
}

func TestUserCloseDuringReloadReportsClosed(t *testing.T) {
	if os.Getenv("DEX_CORE_SILENT_CHILD") == "1" {
		// An MCP server that starts and never answers, so Reload stays in its open step.
		if err := os.WriteFile(os.Getenv("MCP_STARTS"), []byte("start\n"), 0600); err != nil {
			os.Exit(7)
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	// Given a Reload that waits for an MCP server that does not answer.
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	starts := filepath.Join(t.TempDir(), "starts")
	servers := map[string]any{"mcpServers": map[string]any{
		"silent": map[string]any{"command": binary, "args": []string{"-test.run=^TestUserCloseDuringReloadReportsClosed$"}, "env": map[string]string{"DEX_CORE_SILENT_CHILD": "1", "MCP_STARTS": starts}},
	}}
	raw, err := json.Marshal(servers)
	if err != nil {
		t.Fatal(err)
	}
	extensionFile(t, filepath.Join(config.DataDir, "agents", "alpha", ".agents", "mcp.json"), string(raw))
	agents := coreEngine(t, config)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	reloaded := make(chan error, 1)
	go func() { reloaded <- agents.Reload(ctx, "alpha") }()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(starts); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the MCP server did not start")
		}
	}

	// When the engine closes while Reload opens the agent.
	if err := agents.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// Then Reload reports that the engine closed, not a cancellation that the caller did not ask for.
	if err := <-reloaded; !errors.Is(err, ErrClosed) {
		t.Fatalf("Reload interrupted by Close: %v, want ErrClosed", err)
	}
}
