package engine

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
	storage "github.com/MichaelKinsy/PiG/durable/storage/sqlite/node"
)

func TestUserGroupExecutionGateBlocksRegisteredDirectNestedDeferredAndOwnedChildCalls(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"direct", "nested-deferred", "owned-child"} {
		t.Run(mode, func(t *testing.T) {
			// Given a hostile native registry with an executable forbidden tool, not only a hidden schema.
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			catalog := NewCatalog(true)
			defer catalog.Close()
			marker := filepath.Join(dir, "escaped")
			group := groupExtension(root)
			forbidden := &durable.ToolRegistration{Name: "forbidden_group_probe", Description: "Adversarial native tool that writes a real canary file.", Replay: durable.ReplaySafe, Deferred: mode == "nested-deferred", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}, Execute: func(context.Context, any, durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
				err := os.WriteFile(marker, []byte("executed"), 0600)
				return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: "executed"}}}, err
			}}
			if mode == "nested-deferred" {
				group.Tools[1].Execute = func(ctx context.Context, _ any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
					nested, err := api.ExecuteTool(ctx, forbidden.Name, map[string]any{})
					if err != nil {
						return durable.ToolExecutionResult{}, err
					}
					body, err := json.Marshal(nested)
					return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: string(body)}}}, err
				}
			}
			group.Tools = append(group.Tools, forbidden)
			type state struct {
				Phase string `json:"phase"`
			}
			owner := durable.DefineTask(durable.TaskDefinition[struct{}, state, durable.JsonValue, any]{Name: "group-probe-owner", Version: 1, Initial: func(struct{}) state { return state{Phase: "wait"} }, Phases: map[string]durable.PhaseHandler[struct{}, state, durable.JsonValue, any]{"wait": func(context.Context, durable.RunningTask[struct{}, state, durable.JsonValue], durable.TaskRuntime[struct{}, state, durable.JsonValue, any]) error {
				return nil
			}}, Abort: func(context.Context, durable.RunningTask[struct{}, state, durable.JsonValue], durable.TaskRuntime[struct{}, state, durable.JsonValue, any]) error {
				return nil
			}})
			// This owner stays pending because the registry has no executable definition for it.
			registry := harness.CreateRegistry()
			if err := registry.Install(group); err != nil {
				t.Fatal(err)
			}
			open := func() harness.Harness {
				store, err := storage.OpenNodeSqliteStorage(filepath.Join(dir, "state.sqlite"), storage.NodeSqliteStorageOptions{})
				if err != nil {
					t.Fatal(err)
				}
				h, err := harness.OpenHarness(t.Context(), store, harness.HarnessOptions{Models: catalog.Models, Registry: registry, Settings: func() *harness.HarnessSettings {
					return &harness.HarnessSettings{Extensions: []*durable.Extension{group}}
				}})
				if err != nil {
					t.Fatal(err)
				}
				return h
			}
			h := open()
			defer func() { _ = h.Close(t.Context()) }()
			change := agentChange(coreOptions("direct"))
			change.Tools = harness.SetTo(harness.ToolChange{List: group.Tools})
			offered := group.Tools
			if mode == "nested-deferred" {
				offered = group.Tools[:len(group.Tools)-1]
			}
			change.ModelTools = harness.SetTo(harness.ToolChange{List: offered})
			parent, err := h.CreateConversation(t.Context(), harness.ConversationCreateOptions{Ownership: durable.ConversationOwnership{Kind: durable.ConversationOwnerless}, Agent: &change})
			if err != nil {
				t.Fatal(err)
			}
			conversation := parent
			if mode == "owned-child" {
				parentID := parent.Id()
				childID, err := durable.Commit(t.Context(), h, func(tx durable.Tx) (durable.ConversationId, error) {
					taskID, err := durable.CreateTask(tx, owner, struct{}{}, durable.TaskOptions{Ownership: durable.TaskOwnership{Kind: durable.TaskOwnedByConversation}, ConversationId: &parentID})
					if err != nil {
						return 0, err
					}
					child, err := tx.CreateConversation(durable.CreateConversationOptions{Ownership: durable.ConversationOwnership{Kind: durable.ConversationOwnedByTask, TaskId: taskID}})
					return child.Id, err
				})
				if err != nil {
					t.Fatal(err)
				}
				conversation, err = h.Conversation(t.Context(), childID)
				if err != nil {
					t.Fatal(err)
				}
				if err := conversation.Configure(t.Context(), harness.AgentChange{Model: harness.SetTo(durable.ModelRef{Provider: "faux", ModelId: "direct"})}); err != nil {
					t.Fatal(err)
				}
			}
			h.Resume()
			// When a direct, nested/deferred or task-owned child call reaches actual native dispatch.
			content := `forbidden_group_probe {}`
			if mode == "nested-deferred" {
				content = `group_note {"text":"nested attack"}`
			}
			run := func(c harness.Conversation, request string) {
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				submitted, err := c.Submit(ctx, durable.SubmissionDraft{Type: durable.SubmissionTypeInput, RequestId: &request, Content: ai.UserText(content)})
				if err != nil {
					t.Fatal(err)
				}
				result, err := submitted.Wait(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if result.Status != durable.SubmissionDone || result.Answer == nil {
					reason := ""
					if result.Reason != nil {
						reason = *result.Reason
					}
					t.Fatalf("native attack outcome=%+v reason=%s", result, reason)
				}
				var answer *durable.EntryRecord
				_, err = c.Commit(ctx, func(tx durable.Tx) (any, error) {
					var err error
					answer, err = tx.Entry(*result.Answer)
					return nil, err
				})
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(answerText(answer.Model), "not allowed") {
					t.Fatalf("execution gate did not refuse %s: %s", mode, answerText(answer.Model))
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("forbidden native handler ran in %s: %v", mode, err)
				}
			}
			run(conversation, "first")
			// Then restore retains the executable gate, including the child selection and deferred tools.
			id := conversation.Id()
			if err := h.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			h = open()
			conversation, err = h.Conversation(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			h.Resume()
			run(conversation, "restored")
		})
	}
}
