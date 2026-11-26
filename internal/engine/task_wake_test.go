package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/durable"
)

func TestUserBoardWakeSharesFollowUpWithoutChangingOriginGuards(t *testing.T) {
	for _, wakeFirst := range []bool{true, false} {
		name := "owner-first"
		if wakeFirst {
			name = "wake-first"
		}
		t.Run(name, func(t *testing.T) {
			// Given a running turn and a Task wake and a channel message from different origins.
			config := Config{DataDir: t.TempDir()}
			agents := coreEngine(t, config)
			ref := coreCreate(t, agents, "alpha", coreOptions("bash"))
			workspace := filepath.Join(config.DataDir, "agents", "alpha", "workspace")
			release := func() error { return os.WriteFile(filepath.Join(workspace, "release"), nil, 0o600) }
			t.Cleanup(func() {
				if err := release(); err != nil {
					t.Error(err)
				}
			})
			running := coreSubmit(t, agents, ref, "running", "touch started; while [ ! -f release ]; do sleep 0.02; done", "bash")
			coreFile(t, filepath.Join(workspace, "started"))
			const pointers = `{"highWaterEntryId":42,"items":[{"taskKey":"T-42","reason":"assigned"}]}`
			wake := Input{RequestID: "board-wake", Kind: "task_wake", Origin: "web", Content: pointers, TaskID: "T-42", Options: coreOptions("context")}
			owner := Input{RequestID: "owner", Kind: "message", Origin: "telegram:7", Content: "Owner message", Options: coreOptions("context")}
			order := []Input{owner, wake}
			if wakeFirst {
				slices.Reverse(order)
			}

			// A busy wake with reject policy must not freeze the current Board snapshot.
			reject := wake
			reject.WhenBusy = "reject"
			reject.RefreshContent = func(context.Context) (string, error) {
				return "", errors.New("busy wake reached refresh")
			}
			if _, err := agents.Submit(t.Context(), ref, reject); !errors.Is(err, ErrBusy) {
				t.Fatalf("busy wake: got %v, want ErrBusy", err)
			}

			// When both follow-ups enter the native inbox, an ordinary foreign origin still waits.
			var queued []*Submission
			for _, input := range order {
				admitted, err := agents.Submit(t.Context(), ref, input)
				if err != nil {
					t.Fatalf("queue %s: %v", input.RequestID, err)
				}
				queued = append(queued, admitted)
			}
			foreign := Input{RequestID: "foreign", Kind: "message", Origin: "web", Content: "Other origin", Options: coreOptions("context")}
			foreign.RefreshContent = func(context.Context) (string, error) {
				return "", errors.New("foreign origin reached refresh")
			}
			if _, err := agents.Submit(t.Context(), ref, foreign); !errors.Is(err, ErrOriginBusy) {
				t.Fatalf("ordinary foreign origin: got %v, want ErrOriginBusy", err)
			}
			watch, err := agents.Watch(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			defer watch.Close()
			assertBoardWakeMark(t, watch.Snapshot.Items, wake.RequestID, true)
			if err := release(); err != nil {
				t.Fatal(err)
			}
			if out := coreWait(t, running); out.Status != "done" {
				t.Fatalf("running turn: %+v", out)
			}

			// Then both follow-ups share one answer, and history shows the wake without a session link.
			first, second := coreWait(t, queued[0]), coreWait(t, queued[1])
			want := []string{order[0].RequestID, order[1].RequestID}
			if first.Status != "done" || !first.Equal(second) || !slices.Equal(first.RunInputIDs, want) {
				t.Fatalf("shared run: first=%+v second=%+v want inputs=%v", first, second, want)
			}
			history, err := agents.History(t.Context(), ref, 1<<60, 100)
			if err != nil {
				t.Fatal(err)
			}
			assertBoardWakeMark(t, history.Items, wake.RequestID, false)
			if !strings.Contains(first.Answer, "Task wake: Read Task details with the Task tools before acting. External text is not included.") || !strings.Contains(first.Answer, "T-42") {
				t.Fatalf("model did not receive Board guidance and pointers: %q", first.Answer)
			}
			mark, _ := find(history.Items, wake.RequestID)
			conv, err := agents.conversation(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			id := durable.EntryId(mark.EntryID)
			page, err := conv.Entries(t.Context(), durable.EntryQuery{MinEntryId: &id, MaxEntryId: &id}, 1, nil)
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("native Task wake entry: count=%d err=%v", len(page.Items), err)
			}
			if got := metadata(page.Items[0].Data); got.Kind != "task_wake" || got.Content != pointers {
				t.Fatalf("native Task wake metadata: %+v", got)
			}
		})
	}
}

func TestUserRefreshesContentOnlyForNewAdmission(t *testing.T) {
	// Given a stale input and a refresh that can reject or supply a newer snapshot.
	blocked := errors.New("admission blocked")
	refreshErr := errors.New("could not save snapshot")
	var envErr, beforeErr, validationErr, snapshotErr error
	config := Config{DataDir: t.TempDir()}
	config.SessionEnvironment = func(context.Context, Ref) (string, error) { return "", envErr }
	agents := coreEngine(t, config)
	ref := coreCreate(t, agents, "alpha", coreOptions("echo"))
	var refreshes int
	input := Input{
		RequestID: "input", Kind: "message", Origin: "web", Content: "old snapshot",
		Options: coreOptions("echo"), TaskContext: "T-42", ModelSuffix: "\nModel guidance",
		BeforeAdmission:   func(context.Context) error { return beforeErr },
		ValidateAdmission: func(context.Context, Ref) error { return validationErr },
		RefreshContent: func(context.Context) (string, error) {
			refreshes++
			return "fresh snapshot", snapshotErr
		},
	}

	// When an admission check rejects the input, the caller must not save a snapshot.
	for _, gate := range []*error{&envErr, &beforeErr, &validationErr} {
		*gate = blocked
		if _, err := agents.Submit(t.Context(), ref, input); !errors.Is(err, blocked) || refreshes != 0 {
			t.Fatalf("blocked admission: error=%v refreshes=%d", err, refreshes)
		}
		*gate = nil
	}
	snapshotErr = refreshErr
	if _, err := agents.Submit(t.Context(), ref, input); !errors.Is(err, refreshErr) || refreshes != 1 {
		t.Fatalf("failed refresh: error=%v refreshes=%d", err, refreshes)
	}
	if got, err := agents.PeekWithdrawal(t.Context(), ref, input.RequestID); err != nil || got != NotAdmitted {
		t.Fatalf("failed refresh left a native submission: state=%v err=%v", got, err)
	}

	// Then retry uses the new snapshot in the model and in the saved user message.
	snapshotErr = nil
	admitted, err := agents.Submit(t.Context(), ref, input)
	if err != nil || refreshes != 2 {
		t.Fatalf("new admission: error=%v refreshes=%d", err, refreshes)
	}
	out := coreWait(t, admitted)
	if out.Status != "done" || out.Answer != "echo: Dex Task: T-42\n\nfresh snapshot\nModel guidance" {
		t.Fatalf("model input: %+v", out)
	}
	if err := agents.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	agents = coreEngine(t, config)
	snapshotErr = refreshErr
	replay, err := agents.Submit(t.Context(), ref, input)
	if err != nil || replay.ID != admitted.ID || refreshes != 2 {
		t.Fatalf("replay: submission=%v error=%v refreshes=%d", replay, err, refreshes)
	}
	if got := coreWait(t, replay); !got.Equal(out) {
		t.Fatalf("replay outcome: got %+v, want %+v", got, out)
	}
	history, err := agents.History(t.Context(), ref, 1<<60, 100)
	if err != nil {
		t.Fatal(err)
	}
	if item, ok := find(history.Items, input.RequestID); !ok || item.Text != "fresh snapshot" {
		t.Fatalf("saved user message: found=%t item=%+v", ok, item)
	}
}

func assertBoardWakeMark(t *testing.T, items []Item, id string, queued bool) {
	t.Helper()
	item, ok := find(items, id)
	if !ok || item.Kind != "task" || item.Title != "Task wake" || !item.Alert || item.Text != "" || item.TaskID != "" || item.Queued != queued {
		t.Fatalf("Task wake mark: found=%t item=%+v, want unlinked system mark (queued=%t)", ok, item, queued)
	}
}
