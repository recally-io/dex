package chat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserStoppedInputCannotCreatePendingToolCompaction(t *testing.T) {
	r := startNative(t, "bash")
	started := filepath.Join(r.dir, "started")
	id, err := r.service.Post(t.Context(), "bot", MainKey, "", "request", "touch "+started+"; while :; do sleep 1; done")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(started); return err == nil })
	c, err := r.service.Get(t.Context(), "bot", id)
	if err != nil {
		t.Fatal(err)
	}
	r.service.cancel()
	r.service.wg.Wait()
	if err = r.agents.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	source := engine.TaskContext{AgentID: "bot", ConversationID: c.ExecutionID, SourceRequestID: "request", Key: "bot/late"}
	if _, err = r.service.ToolCompact(t.Context(), source); !errors.Is(err, engine.ErrClosed) {
		t.Fatalf("expected failed native commit: %v", err)
	}
	if err = r.service.Stop(t.Context(), "bot", id); !errors.Is(err, engine.ErrClosed) {
		t.Fatalf("expected deferred abort: %v", err)
	}
	next, err := engine.New(engine.Config{DataDir: r.dir, Home: t.TempDir(), BaseEnv: map[string]string{"PATH": os.Getenv("PATH")}, FakeModel: true})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close(context.Background())
	service := New(r.conn, next, time.Now, nil, nil)
	defer func() { service.cancel(); service.wg.Wait() }()
	if err = service.RecoverTasks(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = service.reconcileAgent(t.Context(), "bot"); err != nil {
		t.Fatal(err)
	}
	op, err := r.q.GetCompactOperation(t.Context(), "bot:late")
	if err != nil || op.Status != "rejected" || op.TaskID.Valid {
		t.Fatalf("stopped compact created or stayed pending: %+v %v", op, err)
	}
}

func TestUserStoppedInputRecoversCommittedToolCompaction(t *testing.T) {
	r := startNative(t, "bash")
	started := filepath.Join(r.dir, "started")
	id, err := r.service.Post(t.Context(), "bot", MainKey, "", "request", "touch "+started+"; while :; do sleep 1; done")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, err := os.Stat(started); return err == nil })
	c, err := r.service.Get(t.Context(), "bot", id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.conn.ExecContext(t.Context(), `CREATE TRIGGER compact_ack_failure BEFORE UPDATE OF status ON compact_operations WHEN NEW.status='admitted' BEGIN SELECT RAISE(FAIL, 'compact ack unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	source := engine.TaskContext{AgentID: "bot", ConversationID: c.ExecutionID, SourceRequestID: "request", Key: "bot/saved"}
	if _, err = r.service.ToolCompact(t.Context(), source); err == nil {
		t.Fatal("expected acknowledgement failure")
	}
	nativeID, found, err := r.agents.LookupCompact(t.Context(), engine.Ref{AgentID: "bot", ID: c.ExecutionID}, "bot:saved")
	if err != nil || !found || nativeID == 0 {
		t.Fatalf("native id=%d found=%v err=%v", nativeID, found, err)
	}
	if err = r.service.Stop(t.Context(), "bot", id); err != nil {
		t.Fatal(err)
	}
	if _, err = r.conn.ExecContext(t.Context(), `DROP TRIGGER compact_ack_failure`); err != nil {
		t.Fatal(err)
	}
	if err = r.service.reconcileAgent(t.Context(), "bot"); err != nil {
		t.Fatal(err)
	}
	op, err := r.q.GetCompactOperation(t.Context(), "bot:saved")
	if err != nil || op.Status != "admitted" || op.TaskID.Int64 != nativeID {
		t.Fatalf("committed compact lost: %+v %v", op, err)
	}
	replay, err := r.service.ToolCompact(t.Context(), source)
	if err != nil || replay.ID != "bot:saved" {
		t.Fatalf("saved replay=%+v err=%v", replay, err)
	}
}

func TestUserStoppedInputRecoversStillRunningCompaction(t *testing.T) {
	r := startNative(t, "echo")
	answerReached, summaryReached := make(chan struct{}, 1), make(chan struct{}, 1)
	answerGate, summaryGate := make(chan struct{}), make(chan struct{})
	var answerOnce, summaryOnce sync.Once
	defer answerOnce.Do(func() { close(answerGate) })
	defer summaryOnce.Do(func() { close(summaryGate) })
	faux := ai.NewFauxProvider(ai.FauxConfig{ProviderID: "compact-gate", Models: []ai.FauxModelDefinition{{ID: "model", ContextWindow: 1000000, MaxTokens: 900}}, TokensPerSecond: 10000})
	steps := make([]ai.FauxResponseStep, 30)
	for i := range steps {
		steps[i] = ai.FauxFactoryStep(func(tctx ai.TranscriptContext, options ai.StreamOptions, _ *ai.FauxProviderState, _ *ai.Model) (ai.AssistantMessage, error) {
			summary := false
			messages := tctx.Messages()
			if len(messages) > 0 {
				if m, ok := messages[0].(ai.SystemMessage); ok {
					if text, ok := m.Content.(ai.SystemText); ok {
						summary = strings.HasPrefix(string(text), "You are a context summarization assistant")
					}
				}
			}
			var gate <-chan struct{}
			if summary {
				select {
				case summaryReached <- struct{}{}:
				default:
				}
				gate = summaryGate
			} else {
				for _, m := range messages {
					if u, ok := m.(ai.UserMessage); ok {
						if text, ok := u.Content.(ai.UserText); ok && string(text) == "hold" {
							select {
							case answerReached <- struct{}{}:
							default:
							}
							gate = answerGate
						}
					}
				}
			}
			if gate != nil {
				select {
				case <-gate:
				case <-options.Signal.Done():
					return ai.AssistantMessage{}, options.Signal.Err()
				}
			}
			return (ai.FauxResponse{Content: []ai.FauxContentBlock{ai.FauxText("short answer")}}).AssistantMessage(), nil
		})
	}
	faux.SetResponses(steps)
	r.agents.Catalog.Models.SetProvider(faux.Provider())
	if err := r.q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{ID: "bot", Name: "Bot", ModelProvider: "compact-gate", ModelID: "model", ThinkingLevel: "off"}); err != nil {
		t.Fatal(err)
	}
	var id int64
	for i := range 3 {
		request := fmt.Sprintf("history-%d", i)
		var err error
		id, err = r.service.Post(t.Context(), "bot", MainKey, "", request, strings.Repeat("x", 110000))
		if err != nil {
			t.Fatal(err)
		}
		if out := awaitReceipt(t, r, request); out.Status != "done" {
			t.Fatalf("history: %+v", out)
		}
	}
	if _, err := r.service.Post(t.Context(), "bot", MainKey, "", "hold", "hold"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-answerReached:
	case <-time.After(10 * time.Second):
		t.Fatal("answer did not block")
	}
	c, err := r.service.Get(t.Context(), "bot", id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.conn.ExecContext(t.Context(), `CREATE TRIGGER compact_ack_failure BEFORE UPDATE OF status ON compact_operations WHEN NEW.status='admitted' BEGIN SELECT RAISE(FAIL, 'compact ack unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	source := engine.TaskContext{AgentID: "bot", ConversationID: c.ExecutionID, SourceRequestID: "hold", Key: "bot/live"}
	if _, err = r.service.ToolCompact(t.Context(), source); err == nil {
		t.Fatal("expected acknowledgement failure")
	}
	select {
	case <-summaryReached:
	case <-time.After(10 * time.Second):
		t.Fatal("summary did not block")
	}
	ref := engine.Ref{AgentID: "bot", ID: c.ExecutionID}
	taskID, found, err := r.agents.LookupCompact(t.Context(), ref, "bot:live")
	if err != nil || !found {
		t.Fatalf("task=%d found=%v err=%v", taskID, found, err)
	}
	state, err := r.agents.CompactionStatus(t.Context(), ref, taskID)
	if err != nil || state.Status != "generating" {
		t.Fatalf("task not running: %+v %v", state, err)
	}
	if err = r.service.Stop(t.Context(), "bot", id); err != nil {
		t.Fatal(err)
	}
	if _, err = r.conn.ExecContext(t.Context(), `DROP TRIGGER compact_ack_failure`); err != nil {
		t.Fatal(err)
	}
	if err = r.service.reconcileAgent(t.Context(), "bot"); err != nil {
		t.Fatal(err)
	}
	op, err := r.q.GetCompactOperation(t.Context(), "bot:live")
	if err != nil || op.Status != "admitted" || op.TaskID.Int64 != taskID {
		t.Fatalf("running admitted task lost: %+v %v", op, err)
	}
	t.Logf("running native compaction %d remained admitted after Stop", taskID)
}
