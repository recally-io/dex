package engine

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserUpgradesPiG053StoreWithoutLosingHistoryOrTaskBoundaries(t *testing.T) {
	// Given a real v0.5.3 store with reset history, a sealed task and a running tool.
	config := Config{DataDir: t.TempDir(), Home: t.TempDir()}
	dir := filepath.Join(config.DataDir, "agents", "upgrade")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/pig-v0.5.3/state.sql")
	if err != nil {
		t.Fatal(err)
	}
	store, err := sql.Open("sqlite", filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, loadErr := store.ExecContext(t.Context(), string(fixture))
	if err := errors.Join(loadErr, store.Close()); err != nil {
		t.Fatal(err)
	}
	a := coreEngine(t, config)
	chat := Ref{AgentID: "upgrade", ID: 2}
	task := Ref{AgentID: "upgrade", ID: 29}
	running := Ref{AgentID: "upgrade", ID: 41}
	outcome, err := a.RecoveryOutcome(t.Context(), running, "legacy-running-input")
	if err != nil || outcome != nil {
		t.Fatalf("unfinished legacy task: %+v, %v", outcome, err)
	}
	if err := a.Recover(t.Context(), []Ref{running}); err != nil {
		t.Fatal(err)
	}

	// When the new engine opens the old store, history and the active context remain readable.
	history, err := a.History(t.Context(), chat, 1<<62, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, item := range history.Items {
		if item.Kind == "user" {
			found[item.ID] = true
		}
		if item.Kind == "tool" && item.Name == "bash" && item.Status == "done" && item.Output == "legacy-tool" {
			found["tool-result"] = true
		}
	}
	for _, id := range []string{"before-reset", "after-reset", "legacy-tool", "tool-result"} {
		if !found[id] {
			t.Fatalf("old history lost %s: %+v", id, history.Items)
		}
	}
	watch, err := a.Watch(t.Context(), chat)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	activeInput := false
	for _, item := range watch.Snapshot.Items {
		if item.ID == "before-reset" {
			t.Fatal("old input before the reset entered the active context")
		}
		activeInput = activeInput || item.ID == "after-reset"
	}
	if !activeInput {
		t.Fatalf("old active context is missing: %+v", watch.Snapshot)
	}
	replay, err := a.Submit(t.Context(), chat, Input{RequestID: "before-reset"})
	if err != nil {
		t.Fatal(err)
	}
	if got := coreWait(t, replay); got.Status != "done" || got.Answer != "echo: before reset" {
		t.Fatalf("old input replay: %+v", got)
	}
	if got := coreWait(t, coreSubmit(t, a, chat, "after-upgrade", "after upgrade", "echo")); got.Answer != "echo: after upgrade" || got.Status != "done" {
		t.Fatalf("new input after upgrade: %+v", got)
	}

	// Then the old sealed task accepts continuation without restoring report or steer authority.
	continued, err := a.Submit(t.Context(), task, Input{
		RequestID: "task-after-upgrade", TaskContinuation: true, Options: coreOptions("tool"),
		Content: `report_task {"outcome":"done","summary":"replace old report","cause":"","reproduction":[],"files":[],"openItems":[]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := coreWait(t, continued); got.Status != "done" || !strings.HasPrefix(got.Answer, "tool error: ") {
		t.Fatalf("continued task regained report authority: %+v", got)
	}
	if _, err := a.Submit(t.Context(), task, Input{
		RequestID: "late-steer", Content: "replace goal", Steer: &TrustedTaskSteer{OriginalRequestID: "legacy-task-input"},
	}); err == nil {
		t.Fatal("upgrade reopened the sealed task")
	}
	interrupted, err := a.Submit(t.Context(), running, Input{RequestID: "legacy-running-input", Background: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := coreWait(t, interrupted); got.Status != "unanswered" {
		t.Fatalf("old running task resumed instead of being isolated: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "workspace", "upgrade-replayed-task")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old tool ran again during recovery: %v", err)
	}
	watch.Close()
	if err := a.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Reopening the migrated store retains the new answer under the same request ID.
	a = coreEngine(t, config)
	replay, err = a.Submit(t.Context(), chat, Input{RequestID: "after-upgrade"})
	if err != nil {
		t.Fatal(err)
	}
	if got := coreWait(t, replay); got.Answer != "echo: after upgrade" || got.Status != "done" {
		t.Fatalf("migrated store replay: %+v", got)
	}
}
