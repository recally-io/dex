package engine

import (
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
)

func coreEngine(t *testing.T, config Config) *Agents {
	t.Helper()
	config.FakeModel = true
	if config.DataDir == "" {
		config.DataDir = t.TempDir()
	}
	if config.Home == "" {
		config.Home = t.TempDir()
	}
	if config.BaseEnv == nil {
		config.BaseEnv = map[string]string{"PATH": "/usr/bin:/bin"}
	}
	agents, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := agents.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return agents
}
func coreOptions(model string) Options {
	return Options{Model: ModelRef{Provider: "faux", ModelID: model}, ThinkingLevel: "off"}
}
func coreCreate(t *testing.T, a *Agents, agent string, options Options) Ref {
	t.Helper()
	ref, err := a.Create(t.Context(), agent, options)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}
func coreSubmit(t *testing.T, a *Agents, ref Ref, id, text, model string) *Submission {
	t.Helper()
	s, err := a.Submit(t.Context(), ref, Input{RequestID: id, Content: text, Kind: "message", Origin: "web", Source: "dex:" + ref.AgentID + "/chat/1", Options: coreOptions(model)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func coreWait(t *testing.T, s *Submission) Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	outcome, err := s.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return outcome
}
func coreFile(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("file was not created: %s", path)
		case <-tick.C:
		}
	}
}

func TestUserNativeSubmissionSurvivesReopen(t *testing.T) {
	// Given a native Harness and real SQLite with an admitted input.
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	a := coreEngine(t, config)
	ref := coreCreate(t, a, "alpha", coreOptions("echo"))
	input := Input{RequestID: "stable", Content: "hello", Kind: "message", Origin: "telegram:7", Source: "dex:alpha/chat/12", Attachments: []Attachment{{ID: "file", Name: "note.txt", Path: "uploads/file/note.txt", MimeType: "text/plain", Size: 5}}, Options: coreOptions("echo")}
	submission, err := a.Submit(t.Context(), ref, input)
	if err != nil {
		t.Fatal(err)
	}
	first := coreWait(t, submission)
	if first.Status != "done" || !strings.Contains(first.Answer, "echo: hello") {
		t.Fatalf("outcome: %+v", first)
	}
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	// When the caller replays the same durable request after reopening.
	reopened := coreEngine(t, config)
	duplicate, err := reopened.Submit(t.Context(), ref, input)
	if err != nil {
		t.Fatal(err)
	}
	// Then the stored answer and input metadata remain stable, without another input entry.
	if duplicate.ID != submission.ID {
		t.Fatalf("submission changed: %d != %d", duplicate.ID, submission.ID)
	}
	if got := coreWait(t, duplicate); got != first {
		t.Fatalf("replayed outcome %+v != %+v", got, first)
	}
	conversation, err := reopened.conversation(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := conversation.Entries(t.Context(), durable.EntryQuery{}, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, entry := range entries.Items {
		if entry.Kind != "dex.user" {
			continue
		}
		users++
		raw, err := json.Marshal(entry.Data)
		if err != nil {
			t.Fatal(err)
		}
		var metadata InputMetadata
		if err := json.Unmarshal(raw, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.Origin != "telegram:7" || metadata.Source != input.Source || len(metadata.Attachments) != 1 {
			t.Fatalf("metadata: %+v", metadata)
		}
	}
	if users != 1 {
		t.Fatalf("input entries: %d", users)
	}
}

func TestUserStopWithdrawsQueuedInputWithoutModelContext(t *testing.T) {
	// Given a real bash tool that keeps the first input running.
	a := coreEngine(t, Config{})
	ref := coreCreate(t, a, "alpha", coreOptions("bash"))
	workspace := filepath.Join(a.config.DataDir, "agents", "alpha", "workspace")
	running := coreSubmit(t, a, ref, "running", "touch started; while :; do sleep 1; done", "bash")
	coreFile(t, filepath.Join(workspace, "started"))
	queued := coreSubmit(t, a, ref, "queued", "withdraw this input", "echo")
	if err := a.Quiesce(t.Context()); !errors.Is(err, ErrBusy) {
		t.Fatalf("maintenance busy: %v", err)
	}
	if err := a.Reload(t.Context(), "beta"); !errors.Is(err, ErrBusy) {
		t.Fatalf("reload accepted another agent's running and queued work: %v", err)
	}
	// When Stop cancels the run and withdraws the queue.
	if err := a.Abort(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if outcome := coreWait(t, running); outcome.Status != "unanswered" {
		t.Fatalf("running outcome %+v", outcome)
	}
	if outcome := coreWait(t, queued); outcome.Status != "unanswered" || outcome.Reason != "aborted" {
		t.Fatalf("queue outcome %+v", outcome)
	}
	// Then history retains one passive entry, and model context excludes the withdrawn input.
	conversation, err := a.conversation(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := conversation.Entries(t.Context(), durable.EntryQuery{}, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	withdrawn := 0
	for _, entry := range entries.Items {
		if entry.Kind == "dex.withdrawn" {
			withdrawn++
			if len(entry.Model) != 0 {
				t.Fatal("withdrawn entry entered model context")
			}
		}
	}
	if withdrawn != 1 {
		t.Fatalf("withdrawn entries: %d", withdrawn)
	}
	if outcome := coreWait(t, coreSubmit(t, a, ref, "after", "after stop", "context")); strings.Contains(outcome.Answer, "withdraw this input") {
		t.Fatalf("withdrawn input reached next model: %s", outcome.Answer)
	}
	if err := a.Reset(t.Context(), ref, "reset-1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Reset(t.Context(), ref, "reset-1"); err != nil {
		t.Fatal(err)
	}
	if outcome := coreWait(t, coreSubmit(t, a, ref, "clean", "clean", "context")); strings.Contains(outcome.Answer, "after stop") {
		t.Fatalf("reset retained prior model context: %s", outcome.Answer)
	}
}

func TestUserBashUsesExplicitAgentEnvironment(t *testing.T) {
	// Given a process-only variable and distinct per-agent environment files.
	t.Setenv("DEX_CORE_PROCESS_SECRET", "must-not-inherit")
	config := Config{DataDir: t.TempDir(), Home: t.TempDir(), BaseEnv: map[string]string{"PATH": "/usr/bin:/bin", "CORE_BASE": "base"}}
	for _, id := range []string{"alpha", "beta"} {
		dir := filepath.Join(config.DataDir, "agents", id, ".agents")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("CORE_AGENT="+id+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a := coreEngine(t, config)
	// When the real bash tool runs for both agents.
	for _, id := range []string{"alpha", "beta"} {
		ref := coreCreate(t, a, id, coreOptions("bash"))
		outcome := coreWait(t, coreSubmit(t, a, ref, "environment", `printf '%s|%s|%s|%s' "$CORE_BASE" "$CORE_AGENT" "${DEX_CORE_PROCESS_SECRET-unset}" "$PWD"`, "bash"))
		// Then each command sees only its explicit environment and its own workspace.
		workspace, err := filepath.EvalSymlinks(filepath.Join(config.DataDir, "agents", id, "workspace"))
		if err != nil {
			t.Fatal(err)
		}
		expected := "base|" + id + "|unset|" + workspace
		if outcome.Status != "done" || outcome.Answer != "bash: "+expected {
			t.Fatalf("environment %s: %+v; want %q", id, outcome, expected)
		}
	}
}

func TestUserMaintenanceGateAndWaitCancellation(t *testing.T) {
	a := coreEngine(t, Config{})
	ref := coreCreate(t, a, "alpha", coreOptions("echo"))
	if err := a.Quiesce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Submit(t.Context(), ref, Input{RequestID: "paused", Options: coreOptions("echo")}); !errors.Is(err, ErrMaintenance) {
		t.Fatalf("paused submission: %v", err)
	}
	a.Resume()
	s := coreSubmit(t, a, ref, "accepted", strings.Repeat("x", 100), "echo")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait: %v", err)
	}
	if outcome := coreWait(t, s); outcome.Status != "done" {
		t.Fatalf("wait cancellation cancelled execution: %+v", outcome)
	}
}

func TestUserRestartDoesNotRepeatBackgroundExecution(t *testing.T) {
	// Given an interrupted background execution with a visible external action.
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	a := coreEngine(t, config)
	ref := coreCreate(t, a, "alpha", coreOptions("bash"))
	input := Input{RequestID: "background", Content: "echo action >> actions; touch started; while :; do sleep 1; done", Kind: "message", Origin: "task", Source: "dex:alpha/task/1", Background: true, Options: coreOptions("bash")}
	original, err := a.Submit(t.Context(), ref, input)
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(config.DataDir, "agents", "alpha", "workspace")
	coreFile(t, filepath.Join(workspace, "started"))
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	// When recovery isolates the execution before the Harness resumes.
	reopened := coreEngine(t, config)
	if err := reopened.Recover(t.Context(), []Ref{ref}); err != nil {
		t.Fatal(err)
	}
	replay, err := reopened.Submit(t.Context(), ref, input)
	if err != nil {
		t.Fatal(err)
	}
	outcome := coreWait(t, replay)
	// Then the original receipt settles unanswered without another external action.
	if replay.ID != original.ID || outcome.Status != "unanswered" {
		t.Fatalf("recovered outcome: %+v", outcome)
	}
	actions, err := os.ReadFile(filepath.Join(workspace, "actions"))
	if err != nil {
		t.Fatal(err)
	}
	if string(actions) != "action\n" {
		t.Fatalf("external action repeated: %q", actions)
	}
}

func TestUserShutdownWaitsForPendingMemoryCommit(t *testing.T) {
	// Given a real git commit held by a clean filter after the model has settled.
	a := coreEngine(t, Config{})
	ref := coreCreate(t, a, "alpha", coreOptions("bash"))
	started := filepath.Join(a.memory.Dir, "commit-started")
	release := filepath.Join(a.memory.Dir, "commit-release")
	script := filepath.Join(a.memory.Dir, ".git", "commit-filter")
	filter := "#!/bin/sh\ntouch '" + started + "'\nwhile [ ! -f '" + release + "' ]; do sleep 0.01; done\ncat\n"
	if err := os.WriteFile(script, []byte(filter), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.memory.git(t.Context(), "config", "filter.gate.clean", script); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.memory.Dir, ".gitattributes"), []byte("MEMORY.md filter=gate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	input := Input{RequestID: "memory", Content: "printf '\\nretained fact\\n' >> '" + filepath.Join(a.memory.Dir, "MEMORY.md") + "'", Kind: "message", Source: "dex:alpha/chat/1", Options: coreOptions("bash")}
	submission, err := a.Submit(t.Context(), ref, input)
	if err != nil {
		t.Fatal(err)
	}
	coreFile(t, started)
	// When maintenance or shutdown arrives while the memory commit is pending.
	if err := a.Quiesce(t.Context()); !errors.Is(err, ErrBusy) {
		t.Fatalf("maintenance accepted pending memory commit: %v", err)
	}
	if err := a.Reload(t.Context(), "beta"); !errors.Is(err, ErrBusy) {
		t.Fatalf("reload accepted pending memory commit: %v", err)
	}
	closeCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := a.Close(closeCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close stopped waiting before cancellation: %v", err)
	}
	// Then Close can be joined after the same durable commit completes.
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if outcome := coreWait(t, submission); outcome.Status != "done" {
		t.Fatalf("memory outcome: %+v", outcome)
	}
	ctx, finish := context.WithTimeout(t.Context(), 10*time.Second)
	defer finish()
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestUserQueuedInputContinuesAfterModelFailure(t *testing.T) {
	// Given a provider response held until the next input is durably queued.
	a := coreEngine(t, Config{})
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	provider := ai.NewFauxProvider(ai.FauxConfig{ProviderID: "failure-boundary", Models: []ai.FauxModelDefinition{{ID: "model"}}, TokensPerSecond: 10000})
	provider.SetResponses([]ai.FauxResponseStep{
		ai.FauxResponseFactory(func(ai.TranscriptContext, ai.StreamOptions, *ai.FauxProviderState, *ai.Model) (ai.AssistantMessage, error) {
			close(entered)
			<-release
			return ai.FauxAssistantMessage(ai.FauxContentString(""), ai.FauxAssistantMessageOptions{StopReason: "error", ErrorMessage: "Invalid request"}), nil
		}),
		ai.FauxAssistantMessage(ai.FauxContentString("next input answered"), ai.FauxAssistantMessageOptions{}),
	})
	a.Catalog.Models.SetProvider(provider.Provider())
	options := Options{Model: ModelRef{Provider: "failure-boundary", ModelID: "model"}, ThinkingLevel: "off"}
	ref := coreCreate(t, a, "alpha", options)
	first, err := a.Submit(t.Context(), ref, Input{RequestID: "first", Content: "first", Options: options})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("provider was not called")
	}
	queued, err := a.Submit(t.Context(), ref, Input{RequestID: "second", Content: "second", Options: options})
	if err != nil {
		t.Fatal(err)
	}
	// When the active model response fails.
	releaseOnce.Do(func() { close(release) })
	// Then the queued input advances without another host submission or progress write.
	if outcome := coreWait(t, first); outcome.Status != "unanswered" {
		t.Fatalf("failed outcome: %+v", outcome)
	}
	if outcome := coreWait(t, queued); outcome.Status != "done" || outcome.Answer != "next input answered" {
		t.Fatalf("next outcome: %+v", outcome)
	}
}
