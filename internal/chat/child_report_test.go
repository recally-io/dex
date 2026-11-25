package chat

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserChildReportIsHandledBeforeSilentExit(t *testing.T) {
	for _, tc := range []struct {
		name, parentKey     string
		action, interrupted bool
	}{
		{name: "actionable", parentKey: MainKey, action: true},
		{name: "no action", parentKey: MainKey},
		{name: "interrupted", parentKey: MainKey, action: true, interrupted: true},
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
			const childTitle = "Local verifier"
			report := "No further work is needed."
			if tc.interrupted {
				report = `"outcome":"interrupted"`
			} else if tc.action {
				report = "The delegated check is ready. Verify the registered child before closing."
			}
			const notice = "The local verifier was interrupted by a restart; I checked its session before reporting."
			steps := []ai.FauxResponseStep{
				step(func(ai.TranscriptContext) ai.AssistantMessage {
					return tool("session_new", map[string]any{"title": childTitle, "message": "Wait for a wake and report internally."})
				}),
				step(func(ai.TranscriptContext) ai.AssistantMessage { return text("Setup recorded.") }),
				step(func(ai.TranscriptContext) ai.AssistantMessage {
					return text("Waiting for the local wake.")
				}),
			}
			if !tc.interrupted {
				steps = append(steps, step(func(req ai.TranscriptContext) ai.AssistantMessage {
					if got := description(req, "session_report"); !strings.Contains(got, "direct parent") || !strings.Contains(got, "Routine") || !strings.Contains(got, "not a user notification") {
						t.Errorf("model did not receive the internal report routing guidance: %q", got)
					}
					return tool("session_report", map[string]any{"content": report})
				}))
			}
			steps = append(steps, step(func(req ai.TranscriptContext) ai.AssistantMessage {
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
				if tc.interrupted {
					_, guidance, found := strings.Cut(rules, "An interrupted result means")
					check := strings.Index(guidance, "Check what already happened")
					notify := strings.Index(guidance, "tell the user what happened via notify_user")
					if !found || check < 0 || notify <= check {
						t.Errorf("model did not receive explicit interrupted-result notification guidance after the side-effect check: %q", guidance)
					}
				}

				last, _ := req.At(req.Len() - 1).(ai.UserMessage)
				if payload, ok := last.Content.(ai.UserText); !ok || !strings.Contains(string(payload), report) {
					t.Error("parent model did not receive the child report")
				}
				if tc.action {
					return tool("session_list", map[string]any{})
				}
				return text("No internal work remains.")
			}))
			if tc.action {
				steps = append(steps, step(func(req ai.TranscriptContext) ai.AssistantMessage {
					result, ok := req.At(req.Len() - 1).(ai.ToolResultMessage)
					if !ok || result.ToolName != "session_list" || result.IsError || len(result.Content) == 0 {
						t.Errorf("parent could not verify its child: %+v", result)
					} else if block, ok := result.Content[0].(ai.TextContent); !ok || !strings.Contains(block.Text, childTitle) {
						t.Errorf("verification did not return %q: %+v", childTitle, result.Content)
					}
					if tc.interrupted {
						return tool("notify_user", map[string]any{"content": notice})
					}
					return text("Verified the delegated child; no user update needed.")
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
			awaitReceipt(t, r, childTurn.ID)
			setup, err := r.q.GetSubmission(t.Context(), "result:"+childTurn.ID)
			if err != nil || setup.ConversationID != parent || !strings.Contains(setup.Content, "Waiting for the local wake.") {
				t.Fatalf("child did not return its setup result to the parent: %+v, %v", setup, err)
			}
			if tc.interrupted {
				if _, err := r.q.SetTelegramSettings(t.Context(), sqlc.SetTelegramSettingsParams{AgentID: "bot", Token: "123:fixture", AllowedUsers: "42"}); err != nil {
					t.Fatal(err)
				}
				if err := r.q.InsertRoutineNotificationTarget(t.Context(), sqlc.InsertRoutineNotificationTargetParams{ID: "target", AgentID: "bot", Platform: "telegram", AccountID: "123", Peer: "telegram:42"}); err != nil {
					t.Fatal(err)
				}
				if _, err := r.q.SetPrimaryNotificationTarget(t.Context(), sqlc.SetPrimaryNotificationTargetParams{ID: "bot", PrimaryNotificationTargetID: sql.NullString{String: "target", Valid: true}}); err != nil {
					t.Fatal(err)
				}
			}
			var outboxBefore, outboxAfter int
			if err := r.conn.QueryRowContext(t.Context(), `SELECT count(*) FROM outbox_messages`).Scan(&outboxBefore); err != nil {
				t.Fatal(err)
			}

			// When a child System Turn reports to its direct parent, rather than to a channel.
			const wakeID = "local-child-wake"
			origin := "telegram:42"
			if tc.interrupted {
				origin = "task" // The interrupted wake is internal and has no channel route.
			}
			if _, err := r.q.CreateSubmission(t.Context(), sqlc.CreateSubmissionParams{
				ID: wakeID, ConversationID: childTurn.ConversationID, Content: "Local check finished.", Attachments: "null", Origin: origin, Producer: ProducerPlugin, Kind: "task_result", CreatedAt: 1,
			}); err != nil {
				t.Fatal(err)
			}
			var reportID string
			if tc.interrupted {
				// Use the real settlement path with a synthetic restart outcome rather than restarting a process.
				if err := r.service.settle(t.Context(), wakeID, engine.Outcome{Status: "unanswered", Reason: "interrupted"}); err != nil {
					t.Fatal(err)
				}
				reportID = "err:" + wakeID
			} else {
				if err := r.service.admit(t.Context(), taskInput(t, r, wakeID)); err != nil {
					t.Fatal(err)
				}
				if got := awaitReceipt(t, r, wakeID); got.ExitAction != "report_parent" {
					t.Fatalf("child did not report: %+v", got)
				}
				if err := r.conn.QueryRowContext(t.Context(), `SELECT id FROM submissions WHERE conversation_id = ? AND id LIKE 'report:%' AND kind = 'task_result'`, parent).Scan(&reportID); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.service.admit(t.Context(), taskInput(t, r, reportID)); err != nil {
				t.Fatal(err)
			}

			// Then normal reports remain silent; an interrupted report may explicitly notify after verification.
			got := awaitReceipt(t, r, reportID)
			if got.Status != "done" {
				t.Fatalf("parent did not finish handling the report: %+v", got)
			}
			if err := r.conn.QueryRowContext(t.Context(), `SELECT count(*) FROM outbox_messages`).Scan(&outboxAfter); err != nil {
				t.Fatal(err)
			}
			if tc.interrupted {
				var kind, peer, body string
				err := r.conn.QueryRowContext(t.Context(), `SELECT kind, peer, body FROM outbox_messages WHERE source_submission_id = ?`, reportID).Scan(&kind, &peer, &body)
				if err != nil || got.ExitAction != "notify_user" || outboxAfter != outboxBefore+1 || kind != "proactive" || peer != "telegram:42" || body != notice {
					t.Fatalf("expected one explicit notification at the default destination, got exit=%q kind=%q peer=%q body=%q new rows=%d err=%v", got.ExitAction, kind, peer, body, outboxAfter-outboxBefore, err)
				}
			} else if outboxAfter != outboxBefore {
				t.Fatalf("internal report created %d Outbox entries", outboxAfter-outboxBefore)
			}
		})
	}
}
