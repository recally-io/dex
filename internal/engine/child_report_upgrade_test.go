package engine_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
	storage "github.com/MichaelKinsy/PiG/durable/storage/sqlite/node"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserUpgradeDeliversAnAdmittedChildReportOnce(t *testing.T) {
	for _, cut := range []string{"before intent", "before report commit", "after report commit"} {
		t.Run(cut, func(t *testing.T) {
			// Given a user's child turn with an admitted call from the old child extension.
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			const agentID, request, content = "bot", "child-input", "Verified before upgrade."
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
			parent, err := q.EnsureConversation(ctx, sqlc.EnsureConversationParams{AgentID: agentID, ChannelKey: "automation:fixture", Title: "Routine"})
			if err != nil {
				t.Fatal(err)
			}
			child, err := q.CreateChildConversation(ctx, sqlc.CreateChildConversationParams{AgentID: agentID, ChannelKey: "child:fixture", Title: "Child", ParentID: sql.NullInt64{Int64: parent.ID, Valid: true}, ScopeContext: "{}"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{ID: request, ConversationID: child.ID, Kind: "message", Origin: "web", Attachments: "null"}); err != nil {
				t.Fatal(err)
			}
			var service *chat.Service
			report := func(ctx context.Context, call engine.TaskContext, args engine.ReportParentArgs) (string, error) {
				return service.ToolReportParent(ctx, call, args)
			}
			agents, err := engine.New(engine.Config{DataDir: dir, Home: t.TempDir(), BaseEnv: map[string]string{"PATH": os.Getenv("PATH")}, FakeModel: true, Tasks: engine.TaskTools{Outbox: engine.OutboxTools{ReportParent: report}}})
			if err != nil {
				t.Fatal(err)
			}
			defer agents.Close(context.WithoutCancel(ctx))
			service = chat.New(conn, agents, time.Now, nil, nil)

			oldModel := ai.NewFauxProvider(ai.FauxConfig{ProviderID: "upgrade", Models: []ai.FauxModelDefinition{{ID: "script"}}, TokensPerSecond: 10000})
			oldModel.SetResponses([]ai.FauxResponseStep{ai.FauxFactoryStep(func(ai.TranscriptContext, ai.StreamOptions, *ai.FauxProviderState, *ai.Model) (ai.AssistantMessage, error) {
				return ai.FauxAssistantMessage(ai.FauxContentBlocks{ai.FauxToolCall("report_parent", map[string]any{"content": content}, nil)}, ai.FauxAssistantMessageOptions{StopReason: "toolUse"}), nil
			})})
			agents.Catalog.Models.SetProvider(oldModel.Provider())
			reached := make(chan string, 1)
			park := func(ctx context.Context, id durable.TaskId) error {
				reached <- fmt.Sprintf("report:%s/%d", agentID, id)
				<-ctx.Done()
				return ctx.Err()
			}
			legacy := &durable.Extension{Name: "child-outbox", Tools: []*durable.ToolRegistration{{
				Name: "report_parent", Description: "Report to the direct parent.", Replay: durable.ReplaySafe,
				Parameters: map[string]any{"type": "object", "required": []string{"content"}, "properties": map[string]any{"content": map[string]any{"type": "string"}, "final": map[string]any{"type": "boolean"}}},
				Execute: func(ctx context.Context, _ any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
					if cut == "after report commit" {
						if _, err := report(ctx, engine.TaskContext{AgentID: agentID, ConversationID: int64(api.ConversationId()), SourceRequestID: request, RunInputIDs: []string{request}, Key: fmt.Sprintf("%s/%d", agentID, api.TaskId())}, engine.ReportParentArgs{Content: content}); err != nil {
							return durable.ToolExecutionResult{}, err
						}
					}
					return durable.ToolExecutionResult{}, park(ctx, api.TaskId())
				},
			}}}
			if cut == "before intent" {
				legacy.Hooks = []durable.HookRegistration{harness.Hook(harness.ToolTask, &harness.ToolHooks{BeforeTool: func(ctx context.Context, _ harness.ToolHookCall, api harness.HookApi) (*harness.BeforeToolResult, error) {
					return nil, park(ctx, api.TaskId())
				}})}
			}
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
			if err := q.SetExecution(ctx, sqlc.SetExecutionParams{ID: child.ID, ExecutionID: sql.NullInt64{Int64: ref.ID, Valid: true}}); err != nil {
				t.Fatal(err)
			}
			data, err := durable.ToJsonValue(map[string]any{"requestId": request, "kind": "message", "origin": "web"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := conv.Submit(ctx, durable.SubmissionDraft{Type: durable.SubmissionTypeInput, RequestId: new(request), Content: ai.UserText("Report the result."), InputEntry: &durable.InputEntry{Kind: "dex.user", Data: data}}); err != nil {
				t.Fatal(err)
			}
			var reportID string
			select {
			case reportID = <-reached:
			case <-ctx.Done():
				t.Fatal("old tool did not reach the restart point", ctx.Err())
			}
			if err := old.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(ctx); err != nil {
				t.Fatal(err)
			}

			// When Dex reopens the same execution and attaches to the saved input.
			oldModel.SetResponses([]ai.FauxResponseStep{ai.FauxFactoryStep(func(ai.TranscriptContext, ai.StreamOptions, *ai.FauxProviderState, *ai.Model) (ai.AssistantMessage, error) {
				t.Error("a replayed final report must end without another model call")
				return ai.FauxResponse{Content: ai.FauxContentBlocks{ai.FauxText("unexpected")}}.AssistantMessage(), nil
			})})
			replay, err := agents.Submit(ctx, ref, engine.Input{RequestID: request})
			if err != nil {
				t.Fatal(err)
			}
			if outcome, err := replay.Wait(ctx); err != nil || outcome.Status != "done" {
				t.Fatalf("recovered child turn = %+v, %v", outcome, err)
			}

			// Then only the original report is in the direct parent's durable inbox.
			var n int
			if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM submissions WHERE kind = 'task_result'`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("report count = %d, want 1; %v", n, err)
			}
			result, err := q.GetSubmission(ctx, reportID)
			if err != nil || result.ConversationID != parent.ID || result.Status != "pending" || !strings.Contains(result.Content, content) {
				t.Fatalf("direct parent report = %+v, %v", result, err)
			}
			source, err := q.GetSubmission(ctx, request)
			if err != nil || source.ExitAction != "report_parent" {
				t.Fatalf("child exit = %q, %v", source.ExitAction, err)
			}
		})
	}
}
