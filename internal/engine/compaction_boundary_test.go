package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
	storage "github.com/MichaelKinsy/PiG/durable/storage/sqlite/node"
)

type compactBoundaryGates struct {
	summaryReached chan struct{}
	summaryRelease chan struct{}
	answerReached  chan struct{}
	answerRelease  chan struct{}
}

func openCompactBoundary(t *testing.T, gates compactBoundaryGates) (harness.Harness, harness.Conversation) {
	t.Helper()
	faux := ai.NewFauxProvider(ai.FauxConfig{Models: []ai.FauxModelDefinition{{ID: "faux-1", ContextWindow: 100000, MaxTokens: 900}}})
	steps := make([]ai.FauxResponseStep, 30)
	for i := range steps {
		steps[i] = ai.FauxFactoryStep(func(transcript ai.TranscriptContext, options ai.StreamOptions, _ *ai.FauxProviderState, _ *ai.Model) (ai.AssistantMessage, error) {
			summary := false
			if messages := transcript.Messages(); len(messages) > 0 {
				if system, ok := messages[0].(ai.SystemMessage); ok {
					if text, ok := system.Content.(ai.SystemText); ok {
						summary = strings.HasPrefix(string(text), "You are a context summarization assistant")
					}
				}
			}
			reached, release := gates.answerReached, gates.answerRelease
			answer := "answer"
			if summary {
				reached, release = gates.summaryReached, gates.summaryRelease
				answer = "SUMMARY"
			}
			if reached != nil {
				select {
				case reached <- struct{}{}:
				default:
				}
			}
			if release != nil {
				select {
				case <-release:
				case <-options.Signal.Done():
					return ai.AssistantMessage{}, options.Signal.Err()
				}
			}
			return (ai.FauxResponse{Content: []ai.FauxContentBlock{ai.FauxText(answer)}}).AssistantMessage(), nil
		})
	}
	faux.SetResponses(steps)
	models := ai.CreateModels()
	models.SetProvider(faux.Provider())
	store, err := storage.OpenNodeSqliteStorage(filepath.Join(t.TempDir(), "state.sqlite"), storage.NodeSqliteStorageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := harness.OpenHarness(t.Context(), store, harness.HarnessOptions{Models: models, Registry: harness.CreateRegistry(), Settings: func() *harness.HarnessSettings {
		return &harness.HarnessSettings{Compaction: &harness.CompactionPolicyPatch{Enabled: new(false), ReserveTokens: new(1000), KeepRecentTokens: new(150), BackgroundTokens: new(0)}}
	}})
	if err != nil {
		t.Fatal(err)
	}
	root, err := h.Root(t.Context(), &harness.RootOptions{Agent: &harness.AgentChange{Model: harness.SetTo(durable.ModelRef{Provider: "faux", ModelId: "faux-1"})}})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		_, err = root.Commit(t.Context(), func(tx durable.Tx) (any, error) {
			return tx.AppendEntry(root.Id(), durable.EntryDraft{Kind: "probe.context", Model: []ai.Message{ai.UserMessage{Content: ai.UserText(strings.Repeat("x", 500))}}})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	h.Resume()
	return h, root
}

func compactBoundarySubmission(t *testing.T, h harness.Harness, id int64) durable.Submission {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	task, err := h.WaitForTask(ctx, durable.TaskId(id))
	if err != nil {
		t.Fatal(err)
	}
	if task.State.Outcome == nil || task.State.Outcome.Status != durable.OutcomeCompleted || task.State.Outcome.Result == nil {
		t.Fatalf("task outcome: %+v", task.State.Outcome)
	}
	result, err := durable.FromJsonValue[harness.CompactionResult](*task.State.Outcome.Result)
	if err != nil {
		t.Fatal(err)
	}
	if result.SubmissionId == nil {
		t.Fatalf("summary missing: %+v", result)
	}
	sub, err := h.Submission(ctx, *result.SubmissionId)
	if err != nil {
		t.Fatal(err)
	}
	return sub
}
func compactBoundaryWait(t *testing.T, sub durable.Submission) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := sub.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}
func compactBoundaryReached(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("provider did not reach gate")
	}
}

func compactBoundaryAgents(t *testing.T, gates compactBoundaryGates) (*Agents, harness.Harness, harness.Conversation, Ref) {
	t.Helper()
	h, c := openCompactBoundary(t, gates)
	a := coreEngine(t, Config{})
	a.resources["alpha"] = &agentResources{harness: h, loaded: &loadedExtensions{}}
	return a, h, c, Ref{AgentID: "alpha", ID: int64(c.Id())}
}

func TestUserCompactionReportsNativeSummaryStates(t *testing.T) {
	t.Run("failed", func(t *testing.T) {
		a, h, c, ref := compactBoundaryAgents(t, compactBoundaryGates{})
		if err := c.Configure(t.Context(), harness.AgentChange{Model: harness.Cleared[durable.ModelRef]()}); err != nil {
			t.Fatal(err)
		}
		id, err := a.Compact(t.Context(), ref, "failed")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		if _, err := h.WaitForTask(ctx, durable.TaskId(id)); err != nil {
			t.Fatal(err)
		}
		got, err := a.CompactionStatus(ctx, ref, id)
		if err != nil || got.Status != "failed" {
			t.Fatalf("status=%+v err=%v", got, err)
		}
	})
	t.Run("applied", func(t *testing.T) {
		a, h, _, ref := compactBoundaryAgents(t, compactBoundaryGates{})
		id, err := a.Compact(t.Context(), ref, "applied")
		if err != nil {
			t.Fatal(err)
		}
		sub := compactBoundarySubmission(t, h, id)
		compactBoundaryWait(t, sub)
		got, err := a.CompactionStatus(t.Context(), ref, id)
		if err != nil || got.Status != "applied" {
			t.Fatalf("status=%+v err=%v", got, err)
		}
	})
	t.Run("stale", func(t *testing.T) {
		reached, release := make(chan struct{}, 1), make(chan struct{})
		a, h, c, ref := compactBoundaryAgents(t, compactBoundaryGates{summaryReached: reached, summaryRelease: release})
		defer func() { close(release) }()
		id, err := a.Compact(t.Context(), ref, "stale")
		if err != nil {
			t.Fatal(err)
		}
		compactBoundaryReached(t, reached)
		got, err := a.CompactionStatus(t.Context(), ref, id)
		if err != nil || got.Status != "generating" {
			t.Fatalf("status=%+v err=%v", got, err)
		}
		if err = c.Reset(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		close(release)
		release = make(chan struct{})
		sub := compactBoundarySubmission(t, h, id)
		compactBoundaryWait(t, sub)
		got, err = a.CompactionStatus(t.Context(), ref, id)
		if err != nil || got.Status != "stale" {
			t.Fatalf("status=%+v err=%v", got, err)
		}
	})
	t.Run("busy", func(t *testing.T) {
		reached, release := make(chan struct{}, 1), make(chan struct{})
		a, h, c, ref := compactBoundaryAgents(t, compactBoundaryGates{answerReached: reached, answerRelease: release})
		defer func() { close(release) }()
		input, err := c.Submit(t.Context(), durable.SubmissionDraft{Type: durable.SubmissionTypeInput, Content: ai.UserText("busy")})
		if err != nil {
			t.Fatal(err)
		}
		compactBoundaryReached(t, reached)
		id, err := a.Compact(t.Context(), ref, "busy")
		if err != nil {
			t.Fatal(err)
		}
		sub := compactBoundarySubmission(t, h, id)
		got, err := a.CompactionStatus(t.Context(), ref, id)
		if err != nil || got.Status != "pending_write" {
			t.Fatalf("status=%+v err=%v", got, err)
		}
		close(release)
		release = make(chan struct{})
		compactBoundaryWait(t, input)
		compactBoundaryWait(t, sub)
		got, err = a.CompactionStatus(t.Context(), ref, id)
		if err != nil || got.Status != "applied" {
			t.Fatalf("status=%+v err=%v", got, err)
		}
	})
}
