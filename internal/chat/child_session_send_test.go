package chat

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/recally-io/dex/internal/db/sqlc"
)

// offeredTools is the tool list that the provider received in a request: each system message adds
// and removes tools.
func offeredTools(req ai.TranscriptContext) map[string]ai.ToolSchema {
	tools := map[string]ai.ToolSchema{}
	for i := range req.Len() {
		msg, ok := req.At(i).(ai.SystemMessage)
		if !ok {
			continue
		}
		for _, removed := range msg.ToolsRemoved {
			delete(tools, removed.Name)
		}
		for _, added := range msg.ToolsAdded {
			tools[added.Name] = added
		}
	}
	return tools
}

func schemaProperties(tool ai.ToolSchema) []string {
	properties, _ := tool.Parameters["properties"].(map[string]any)
	return slices.Sorted(maps.Keys(properties))
}

// toolResults are the results of the newest tool round in a request, by tool name.
func toolResults(req ai.TranscriptContext) map[string]ai.ToolResultMessage {
	results := map[string]ai.ToolResultMessage{}
	for i := req.Len() - 1; i >= 0; i-- {
		result, ok := req.At(i).(ai.ToolResultMessage)
		if !ok {
			break
		}
		results[result.ToolName] = result
	}
	return results
}

func resultText(result ai.ToolResultMessage) string {
	var b strings.Builder
	for _, part := range result.Content {
		if text, ok := part.(ai.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

func TestUserChildSessionSendReportsOnlyToItsDirectParent(t *testing.T) {
	// Given a Routine parent, which is not Main, with one delegated child, and a scripted model.
	r := startNative(t, "echo")
	provider := ai.NewFauxProvider(ai.FauxConfig{ProviderID: "child-send-fixture", Models: []ai.FauxModelDefinition{{ID: "script"}}, TokensPerSecond: 10000})
	r.agents.Catalog.Models.SetProvider(provider.Provider())
	if err := r.q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{
		ID: "bot", Name: "Bot", ModelProvider: "child-send-fixture", ModelID: "script", ThinkingLevel: "off",
	}); err != nil {
		t.Fatal(err)
	}
	calls := func(calls ...ai.FauxContentBlock) ai.AssistantMessage {
		return ai.FauxAssistantMessage(ai.FauxContentBlocks(calls), ai.FauxAssistantMessageOptions{StopReason: "toolUse"})
	}
	call := func(name string, args map[string]any) ai.FauxContentBlock { return ai.FauxToolCall(name, args, nil) }
	text := func(value string) ai.AssistantMessage {
		return ai.FauxResponse{Content: ai.FauxContentBlocks{ai.FauxText(value)}}.AssistantMessage()
	}
	step := func(run func(ai.TranscriptContext) ai.AssistantMessage) ai.FauxResponseStep {
		return ai.FauxFactoryStep(func(req ai.TranscriptContext, _ ai.StreamOptions, _ *ai.FauxProviderState, _ *ai.Model) (ai.AssistantMessage, error) {
			return run(req), nil
		})
	}
	expectError := func(req ai.TranscriptContext, tool, want string) {
		t.Helper()
		result, ok := toolResults(req)[tool]
		if !ok || !result.IsError || !strings.Contains(resultText(result), want) {
			t.Errorf("%s: want an error with %q, got %+v", tool, want, result)
		}
	}
	reports := func(parent int64) []sqlc.Submission {
		t.Helper()
		rows, err := r.conn.QueryContext(t.Context(), `SELECT id, content FROM submissions WHERE conversation_id = ? AND id LIKE 'report:%' AND kind = 'task_result' ORDER BY rowid`, parent)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []sqlc.Submission
		for rows.Next() {
			var s sqlc.Submission
			if err := rows.Scan(&s.ID, &s.Content); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	provider.SetResponses([]ai.FauxResponseStep{
		step(func(req ai.TranscriptContext) ai.AssistantMessage {
			// The parent keeps the dispatch schema of session_send.
			if got := schemaProperties(offeredTools(req)["session_send"]); !slices.Equal(got, []string{"follow_up", "id", "message", "mode"}) {
				t.Errorf("parent session_send schema = %v", got)
			}
			return calls(call("session_new", map[string]any{"title": "Verifier", "message": "Wait for a wake."}))
		}),
		step(func(ai.TranscriptContext) ai.AssistantMessage {
			// The parent cannot use the report form: the native validator rejects it.
			return calls(call("session_send", map[string]any{"content": "Not a parent call"}))
		}),
		step(func(req ai.TranscriptContext) ai.AssistantMessage {
			expectError(req, "session_send", "content: unknown field")
			return text("Setup recorded.")
		}),
		step(func(ai.TranscriptContext) ai.AssistantMessage { return text("Waiting for the wake.") }),
	})
	parent, err := r.service.Post(t.Context(), "bot", "automation:local-fixture", "Routine", "send-setup", "Delegate a check.")
	if err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, "send-setup")
	var childTurnID string
	var childID int64
	if err := r.conn.QueryRowContext(t.Context(), `SELECT s.id, c.id FROM submissions s JOIN conversations c ON c.id = s.conversation_id WHERE c.parent_id = ?`, parent).Scan(&childTurnID, &childID); err != nil {
		t.Fatal(err)
	}
	childTurn := taskInput(t, r, childTurnID)
	if err := r.service.admit(t.Context(), childTurn); err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, childTurn.ID)
	if got := reports(parent); len(got) != 0 {
		t.Fatalf("the parent's own session_send created reports: %+v", got)
	}

	wake := func(id string, steps ...ai.FauxResponseStep) sqlc.Submission {
		t.Helper()
		provider.SetResponses(steps)
		if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{
			ID: id, ConversationID: childID, Content: "Local check finished.", Attachments: "null", Origin: "telegram:42", Kind: "task_result", CreatedAt: 1,
		}); err != nil {
			t.Fatal(err)
		}
		if err := r.service.admit(t.Context(), taskInput(t, r, id)); err != nil {
			t.Fatal(err)
		}
		return awaitReceipt(t, r, id)
	}

	outbox := func() int {
		t.Helper()
		var n int
		if err := r.conn.QueryRowContext(t.Context(), `SELECT count(*) FROM outbox_messages`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("milestone then final report", func(t *testing.T) {
		outboxBefore := outbox()
		// When a child System Turn from a channel origin sends a milestone and then a final report.
		got := wake("wake-final",
			step(func(req ai.TranscriptContext) ai.AssistantMessage {
				tools := offeredTools(req)
				if got := schemaProperties(tools["session_send"]); !slices.Equal(got, []string{"content", "final"}) {
					t.Errorf("child session_send schema = %v", got)
				}
				for _, name := range []string{"report_parent", "session_new", "session_stop", "session_fork", "notify_user"} {
					if _, ok := tools[name]; ok {
						t.Errorf("child was offered %s", name)
					}
				}
				if d := tools["session_send"].Description; !strings.Contains(d, "direct parent") || !strings.Contains(d, "Routine") || !strings.Contains(d, "not a user notification") {
					t.Errorf("child session_send description lacks routing guidance: %q", d)
				}
				return calls(call("session_send", map[string]any{"content": "Half of the checks pass.", "final": false}))
			}),
			step(func(req ai.TranscriptContext) ai.AssistantMessage {
				if result := toolResults(req)["session_send"]; result.IsError || !strings.Contains(resultText(result), "Reported to parent") {
					t.Errorf("milestone did not report: %+v", result)
				}
				return calls(call("session_send", map[string]any{"content": "All checks pass."}))
			}),
			step(func(ai.TranscriptContext) ai.AssistantMessage {
				t.Error("a final session_send did not end the child turn")
				return text("unexpected")
			}),
		)

		// Then the direct parent gets both reports as internal task results, and the turn records the report exit.
		if got.Status != "done" || got.ExitAction != "report_parent" {
			t.Fatalf("child turn = status %q exit %q", got.Status, got.ExitAction)
		}
		sent := reports(parent)
		if len(sent) != 2 || !strings.Contains(sent[0].Content, "Half of the checks pass.") || !strings.Contains(sent[1].Content, "All checks pass.") {
			t.Fatalf("parent reports = %+v", sent)
		}
		// And the user gets no notification from either report.
		if n := outbox() - outboxBefore; n != 0 {
			t.Fatalf("child reports created %d Outbox entries", n)
		}
	})

	t.Run("final report in a batch", func(t *testing.T) {
		before := len(reports(parent))
		// When a final session_send shares its round with another tool call.
		got := wake("wake-batch",
			step(func(ai.TranscriptContext) ai.AssistantMessage {
				return calls(call("session_send", map[string]any{"content": "Batched report"}), call("session_list", map[string]any{}))
			}),
			step(func(req ai.TranscriptContext) ai.AssistantMessage {
				expectError(req, "session_send", "session_send must be the only tool call")
				return text("Kept internal.")
			}),
		)

		// Then the call fails and the parent gets no report.
		if got.ExitAction == "report_parent" || len(reports(parent)) != before {
			t.Fatalf("batched final report was sent: exit %q, reports %+v", got.ExitAction, reports(parent))
		}
	})

	t.Run("wrong form or old name", func(t *testing.T) {
		before := len(reports(parent))
		// When the child tries to address a session, mixes the forms, or uses the old tool name.
		wake("wake-target",
			step(func(ai.TranscriptContext) ai.AssistantMessage {
				return calls(call("session_send", map[string]any{"id": "1", "message": "Go to Main"}))
			}),
			step(func(req ai.TranscriptContext) ai.AssistantMessage {
				expectError(req, "session_send", "id: unknown field")
				return calls(call("session_send", map[string]any{"id": "1", "content": "Mixed"}))
			}),
			step(func(req ai.TranscriptContext) ai.AssistantMessage {
				expectError(req, "session_send", "id: unknown field")
				return calls(call("report_parent", map[string]any{"content": "Old name"}))
			}),
			step(func(req ai.TranscriptContext) ai.AssistantMessage {
				expectError(req, "report_parent", "not available")
				return calls(call("tool_search", map[string]any{"query": "report_parent"}))
			}),
			step(func(req ai.TranscriptContext) ai.AssistantMessage {
				result, ok := toolResults(req)["tool_search"]
				if !ok || result.IsError || strings.Contains(resultText(result), "- report_parent:") {
					t.Errorf("tool search exposed the old tool name or failed: %+v", result)
				}
				return text("Kept internal.")
			}),
		)

		// Then no call reaches the parent or another session.
		if got := reports(parent); len(got) != before {
			t.Fatalf("a rejected call reported: %+v", got)
		}
		var others int
		if err := r.conn.QueryRowContext(t.Context(), `SELECT count(*) FROM submissions WHERE id LIKE 'report:%' AND conversation_id <> ?`, parent).Scan(&others); err != nil || others != 0 {
			t.Fatalf("reports outside the direct parent: %d, %v", others, err)
		}
	})
}
