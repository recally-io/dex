package engine_test

import (
	"context"
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
	storage "github.com/MichaelKinsy/PiG/durable/storage/sqlite/node"
	"github.com/recally-io/dex/internal/automation"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserUpgradeKeepsCommittedAutomationWhenOldToolIsRemoved(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	const agentID, request, oldName = "bot", "create-input", "create_routine"
	dir := t.TempDir()
	conn, err := db.Open(ctx, filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	q := sqlc.New(conn)
	if err := q.CreateAgentIfMissing(ctx, sqlc.CreateAgentIfMissingParams{ID: agentID, Name: "Bot"}); err != nil {
		t.Fatal(err)
	}
	main, err := q.EnsureConversation(ctx, sqlc.EnsureConversationParams{AgentID: agentID, ChannelKey: "main", Title: "Main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{ID: request, ConversationID: main.ID, Kind: "message", Origin: "web", Producer: "user", Attachments: "null"}); err != nil {
		t.Fatal(err)
	}
	tools := automation.NewToolService(conn, automation.New(q, nil, time.Now), nil)
	handlers := automation.ToolHandlers{Create: tools.Create}
	agents, err := engine.New(engine.Config{DataDir: dir, Home: t.TempDir(), BaseEnv: map[string]string{"PATH": os.Getenv("PATH")}, FakeModel: true,
		Extensions: func(rt engine.ExtensionRuntime) engine.ExtensionSet {
			return engine.ExtensionSet{Optional: []*engine.Extension{automation.ToolExtension(rt.AgentID, handlers)}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer agents.Close(context.WithoutCancel(ctx))
	model := ai.NewFauxProvider(ai.FauxConfig{ProviderID: "upgrade", Models: []ai.FauxModelDefinition{{ID: "script"}}, TokensPerSecond: 10000})
	model.SetResponses([]ai.FauxResponseStep{ai.FauxFactoryStep(func(ai.TranscriptContext, ai.StreamOptions, *ai.FauxProviderState, *ai.Model) (ai.AssistantMessage, error) {
		args := map[string]any{"name": "Check", "prompt": "Check for changes", "schedule": map[string]any{"kind": "every", "minutes": 30}, "timeZone": "UTC"}
		return ai.FauxAssistantMessage(ai.FauxContentBlocks{ai.FauxToolCall(oldName, args, nil)}, ai.FauxAssistantMessageOptions{StopReason: "toolUse"}), nil
	})})
	agents.Catalog.Models.SetProvider(model.Provider())

	// Given an old create call that committed in SQLite before PiG saved its result.
	type receipt struct {
		id   string
		call engine.TaskContext
	}
	committed := make(chan receipt, 1)
	legacy := &durable.Extension{Name: "routines", Tools: []*durable.ToolRegistration{
		engine.JSONTool(agentID, oldName, "Create a routine.", engine.ObjectSchema(nil, map[string]any{
			"name": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"}, "schedule": map[string]any{"type": "object"}, "timeZone": map[string]any{"type": "string"},
		}), true, func(ctx context.Context, call engine.TaskContext, patch automation.ToolPatch) (automation.ToolInfo, error) {
			created, err := tools.Create(ctx, call, patch)
			if err != nil {
				return created, err
			}
			committed <- receipt{id: created.ID, call: call}
			<-ctx.Done()
			return created, ctx.Err()
		}),
	}}
	registry := harness.CreateRegistry()
	if err := registry.Install(legacy); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agents", agentID, "state.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := storage.OpenNodeSqliteStorage(path, storage.NodeSqliteStorageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.WithoutCancel(ctx))
	old, err := harness.OpenHarness(ctx, store, harness.HarnessOptions{Registry: registry, Models: agents.Catalog.Models})
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close(context.WithoutCancel(ctx))
	change := harness.AgentChange{Model: harness.SetTo(durable.ModelRef{Provider: "upgrade", ModelId: "script"}), Extensions: harness.SetTo(harness.ExtensionChange{List: []*durable.Extension{legacy}})}
	conv, err := old.CreateConversation(ctx, harness.ConversationCreateOptions{Ownership: durable.ConversationOwnership{Kind: durable.ConversationOwnerless}, Agent: &change})
	if err != nil {
		t.Fatal(err)
	}
	ref := engine.Ref{AgentID: agentID, ID: int64(conv.Id())}
	if err := q.SetExecution(ctx, sqlc.SetExecutionParams{ID: main.ID, ExecutionID: sql.NullInt64{Int64: ref.ID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	data, err := durable.ToJsonValue(map[string]any{"requestId": request, "kind": "message", "origin": "web"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conv.Submit(ctx, durable.SubmissionDraft{Type: durable.SubmissionTypeInput, RequestId: new(request), Content: ai.UserText("Create a check."), InputEntry: &durable.InputEntry{Kind: "dex.user", Data: data}}); err != nil {
		t.Fatal(err)
	}
	var original receipt
	select {
	case original = <-committed:
	case <-ctx.Done():
		t.Fatal("the old tool did not commit", ctx.Err())
	}
	if err := old.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// When Dex opens the saved execution with the renamed tool.
	model.SetResponses([]ai.FauxResponseStep{ai.FauxFactoryStep(func(ai.TranscriptContext, ai.StreamOptions, *ai.FauxProviderState, *ai.Model) (ai.AssistantMessage, error) {
		return ai.FauxResponse{Content: ai.FauxContentBlocks{ai.FauxText("Done.")}}.AssistantMessage(), nil
	})})
	replay, err := agents.Submit(ctx, ref, engine.Input{RequestID: request})
	if err != nil {
		t.Fatal(err)
	}
	if outcome, err := replay.Wait(ctx); err != nil || outcome.Status != "done" {
		t.Fatalf("recovered turn = %+v, %v", outcome, err)
	}

	// Then PiG interrupts the retired call, and the business receipt still returns the same result.
	history, err := agents.History(ctx, ref, math.MaxInt64, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(history.Items, func(item engine.Item) bool {
		return item.Kind == "tool" && item.Name == oldName && item.Status == "error" && strings.Contains(item.Output, "was interrupted")
	}) {
		t.Fatalf("expected an interrupted legacy create in the recovered transcript: %+v", history.Items)
	}
	reopened := automation.NewToolService(conn, automation.New(q, nil, time.Now), nil)
	got, err := reopened.Create(ctx, original.call, automation.ToolPatch{})
	if err != nil || got.ID != original.id {
		t.Fatalf("create receipt = %s, %v; want original automation %s", got.ID, err, original.id)
	}
	rows, err := reopened.List(ctx, original.call)
	if err != nil || len(rows) != 1 || rows[0].ID != original.id {
		t.Fatalf("automation list = %+v, %v; want only %s", rows, err, original.id)
	}
}
