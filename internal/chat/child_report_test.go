package chat

import (
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserChildReportIsHandledBeforeSilentExit(t *testing.T) {
	for _, tc := range []struct {
		name, parentKey string
		action          bool
	}{
		{name: "actionable", parentKey: MainKey, action: true},
		{name: "no action", parentKey: MainKey},
		{name: "routine parent", parentKey: "automation:local-fixture", action: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given a parent with a delegated child and a scripted local model for both.
			r := startNative(t, "echo")
			provider := ai.NewFauxProvider(ai.FauxConfig{ProviderID: "child-report-fixture", Models: []ai.FauxModelDefinition{{ID: "script"}}, TokensPerSecond: 10000})
			r.agents.Catalog.Models.SetProvider(provider.Provider())
			if err := r.q.UpdateAgentSettings(t.Context(), sqlc.UpdateAgentSettingsParams{
				ID: "bot", Name: "Bot", ModelProvider: "child-report-fixture", ModelID: "script", ThinkingLevel: "off",
			}); err != nil {
				t.Fatal(err)
			}

			tool := func(name string, args map[string]any) ai.AssistantMessage {
				return ai.FauxAssistantMessage(ai.FauxContentBlocks{ai.FauxToolCall(name, args, nil)}, ai.FauxAssistantMessageOptions{StopReason: "toolUse"})
			}
			text := func(value string) ai.AssistantMessage {
				return ai.FauxResponse{Content: ai.FauxContentBlocks{ai.FauxText(value)}}.AssistantMessage()
			}
			step := func(run func(ai.TranscriptContext) ai.AssistantMessage) ai.FauxResponseStep {
				return ai.FauxFactoryStep(func(req ai.TranscriptContext, _ ai.StreamOptions, _ *ai.FauxProviderState, _ *ai.Model) (ai.AssistantMessage, error) {
					return run(req), nil
				})
			}
			description := func(req ai.TranscriptContext, name string) string {
				for i := req.Len() - 1; i >= 0; i-- {
					if msg, ok := req.At(i).(ai.SystemMessage); ok {
						for _, tool := range msg.ToolsAdded {
							if tool.Name == name {
								return tool.Description
							}
						}
					}
				}
				return ""
			}
			silent := func(req ai.TranscriptContext, reason string) ai.AssistantMessage {
				if tc.parentKey != MainKey {
					// A Routine does not get the Main/Chat exit tools. Its final text stays internal.
					return text(reason)
				}
				if got := description(req, "stay_silent"); !strings.Contains(got, "Do not use silence to skip internal work") {
					t.Errorf("model did not receive the stay_silent prerequisite: %q", got)
				}
				return tool("stay_silent", map[string]any{"reason": reason})
			}
			const childTitle = "Local verifier"
			report := "No further work is needed."
			if tc.action {
				report = "The delegated check is ready. Verify the registered child before closing."
			}
			steps := []ai.FauxResponseStep{
				step(func(ai.TranscriptContext) ai.AssistantMessage {
					return tool("session_new", map[string]any{"title": childTitle, "message": "Wait for a wake and report internally."})
				}),
				step(func(ai.TranscriptContext) ai.AssistantMessage { return text("Setup recorded.") }),
				step(func(ai.TranscriptContext) ai.AssistantMessage {
					return tool("stay_silent", map[string]any{"reason": "Wait for the local wake."})
				}),
				step(func(req ai.TranscriptContext) ai.AssistantMessage {
					if got := description(req, "report_parent"); !strings.Contains(got, "direct parent") || !strings.Contains(got, "Routine") || !strings.Contains(got, "not a user notification") {
						t.Errorf("model did not receive the internal report routing guidance: %q", got)
					}
					return tool("report_parent", map[string]any{"content": report})
				}),
				step(func(req ai.TranscriptContext) ai.AssistantMessage {
					var rules string
					for _, msg := range req.Messages() {
						if system, ok := msg.(ai.SystemMessage); ok {
							for _, section := range system.Sections {
								if section.Name == "session-rules" {
									rules = ""
									if section.Value != nil {
										rules = *section.Value
									}
								}
							}
						}
					}
					for _, phrase := range []string{"process an incoming child report as internal work first", "record the concrete blocker", "Only then decide", "Do not forward raw child output"} {
						if !strings.Contains(rules, phrase) {
							t.Errorf("model did not receive System Turn guidance %q", phrase)
						}
					}

					last, _ := req.At(req.Len() - 1).(ai.UserMessage)
					if payload, ok := last.Content.(ai.UserText); !ok || !strings.Contains(string(payload), report) {
						t.Error("parent model did not receive the child report")
					}
					if tc.action {
						return tool("session_list", map[string]any{})
					}
					return silent(req, "No internal work remains.")
				}),
			}
			if tc.action {
				steps = append(steps, step(func(req ai.TranscriptContext) ai.AssistantMessage {
					result, ok := req.At(req.Len() - 1).(ai.ToolResultMessage)
					if !ok || result.ToolName != "session_list" || result.IsError || len(result.Content) == 0 {
						t.Errorf("parent could not verify its child: %+v", result)
					} else if block, ok := result.Content[0].(ai.TextContent); !ok || !strings.Contains(block.Text, childTitle) {
						t.Errorf("verification did not return %q: %+v", childTitle, result.Content)
					}
					return silent(req, "Verified the delegated child; no user update needed.")
				}))
			}
			provider.SetResponses(steps)

			parent, err := r.service.Post(t.Context(), "bot", tc.parentKey, "Parent", "report-setup", "Delegate a check; verify its child if action is needed.")
			if err != nil {
				t.Fatal(err)
			}
			awaitReceipt(t, r, "report-setup")
			var childTurnID string
			if err := r.conn.QueryRowContext(t.Context(), `SELECT s.id FROM submissions s JOIN conversations c ON c.id = s.conversation_id WHERE c.parent_id = ?`, parent).Scan(&childTurnID); err != nil {
				t.Fatal(err)
			}
			childTurn := taskInput(t, r, childTurnID)
			if err := r.service.admit(t.Context(), childTurn); err != nil {
				t.Fatal(err)
			}
			if got := awaitReceipt(t, r, childTurn.ID); got.ExitAction != "stay_silent" {
				t.Fatalf("child did not finish setup silently: %+v", got)
			}
			var outboxBefore, outboxAfter int
			if err := r.conn.QueryRowContext(t.Context(), `SELECT count(*) FROM outbox_messages`).Scan(&outboxBefore); err != nil {
				t.Fatal(err)
			}

			// When a child System Turn reports to its direct parent, rather than to a channel.
			const wakeID = "local-child-wake"
			if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{
				ID: wakeID, ConversationID: childTurn.ConversationID, Content: "Local check finished.", Attachments: "null", Origin: "telegram:42", Kind: "task_result", CreatedAt: 1,
			}); err != nil {
				t.Fatal(err)
			}
			if err := r.service.admit(t.Context(), taskInput(t, r, wakeID)); err != nil {
				t.Fatal(err)
			}
			if got := awaitReceipt(t, r, wakeID); got.ExitAction != "report_parent" {
				t.Fatalf("child did not report: %+v", got)
			}
			var reportID string
			if err := r.conn.QueryRowContext(t.Context(), `SELECT id FROM submissions WHERE conversation_id = ? AND id LIKE 'report:%' AND kind = 'task_result'`, parent).Scan(&reportID); err != nil {
				t.Fatal(err)
			}
			if err := r.service.admit(t.Context(), taskInput(t, r, reportID)); err != nil {
				t.Fatal(err)
			}

			// Then the parent settles after verification (or exits directly when there is no action), without producing any Outbox entries.
			if got := awaitReceipt(t, r, reportID); got.Status != "done" || (tc.parentKey == MainKey && got.ExitAction != "stay_silent") {
				t.Fatalf("parent did not settle quietly: %+v", got)
			}
			if err := r.conn.QueryRowContext(t.Context(), `SELECT count(*) FROM outbox_messages`).Scan(&outboxAfter); err != nil {
				t.Fatal(err)
			}
			if outboxAfter != outboxBefore {
				t.Fatalf("internal report created %d Outbox entries", outboxAfter-outboxBefore)
			}
		})
	}
}
