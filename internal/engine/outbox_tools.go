package engine

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strings"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// NotifyUserArgs is the input of the notify_user tool.
type NotifyUserArgs struct {
	Content  string `json:"content"`
	TopicKey string `json:"topicKey,omitempty"`
	Final    *bool  `json:"final,omitempty"`
}

// ReportParentArgs is the input of the child session_send tool, which reports to the direct parent.
type ReportParentArgs struct {
	Content string `json:"content"`
	Final   *bool  `json:"final,omitempty"`
}

// OutboxSummary is one recent delivered Outbox entry rendered into the prompt.
type OutboxSummary struct {
	ID          string
	Kind        string
	Peer        string
	TopicKey    string
	Body        string
	DeliveredAt int64
}

// OutboxTools connects Outbox and System Turn exit-control tools to the chat service.
type OutboxTools struct {
	NotifyUser   func(context.Context, TaskContext, NotifyUserArgs) (string, error)
	ReportParent func(context.Context, TaskContext, ReportParentArgs) (string, error)
	RecentOutbox func(context.Context, Ref) ([]OutboxSummary, error)
}

func boolDefaultTrue(v *bool) bool {
	if v == nil {
		return true
	}
	return *v
}

func outboxExtension(agentID string, tools OutboxTools) *durable.Extension {
	return &durable.Extension{
		Name: "outbox",
		Tools: []*durable.ToolRegistration{
			{
				Name:        "notify_user",
				Description: "Send a message to the user via the durable Outbox. On System Turns (task_result, background wakes, monitors), this is how you report updates to the user. By default (final=true), must be the sole tool call in its round and ends the turn.",
				Parameters: map[string]any{
					"type": "object", "additionalProperties": false, "required": []string{"content"},
					"properties": map[string]any{
						"content":  map[string]any{"type": "string", "minLength": 1, "description": "Message text to send to the user."},
						"topicKey": map[string]any{"type": "string", "description": "Optional stable key to supersede an earlier pending undelivered update on the same topic."},
						"final":    map[string]any{"type": "boolean", "description": "If true (default), ends this turn immediately after sending. Set false to send a progress update and keep working."},
					},
				},
				Replay: durable.ReplaySafe,
				Execute: func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
					if tools.NotifyUser == nil {
						return durable.ToolExecutionResult{}, errors.New("outbox service is unavailable")
					}
					args, err := durable.FromJsonValue[NotifyUserArgs](raw)
					if err != nil {
						return durable.ToolExecutionResult{}, err
					}
					final := boolDefaultTrue(args.Final)
					var call TaskContext
					if final {
						call, err = turnEndingCall(ctx, api, agentID, "notify_user")
					} else {
						call, err = toolCall(ctx, api, agentID)
					}
					if err != nil {
						return durable.ToolExecutionResult{}, err
					}
					outboxID, err := tools.NotifyUser(ctx, call, args)
					if err != nil {
						return durable.ToolExecutionResult{}, err
					}
					data, err := json.Marshal(struct {
						OutboxID string `json:"outboxId"`
						Final    bool   `json:"final"`
					}{OutboxID: outboxID, Final: final})
					if err != nil {
						return durable.ToolExecutionResult{}, err
					}
					res := durable.ToolExecutionResult{
						Output: []ai.ToolResultMessageContent{ai.TextContent{Text: "Sent via Outbox: " + string(data)}},
					}
					if final {
						res.Control = &durable.ToolControl{Terminate: true}
					}
					return res, nil
				},
			},
		},
		Sections: []*durable.PromptSection{
			harness.Section("recent-outbox", func(ctx context.Context, input durable.PromptInput) (*string, error) {
				if tools.RecentOutbox == nil {
					return nil, nil
				}
				rows, err := tools.RecentOutbox(ctx, Ref{AgentID: agentID, ID: int64(input.ConversationId)})
				if err != nil || len(rows) == 0 {
					return nil, err
				}
				var b strings.Builder
				b.WriteString("Recent messages already delivered to the user in this conversation (newest first):")
				for _, r := range rows {
					b.WriteString("\n- [")
					b.WriteString(r.Kind)
					if r.Peer != "" {
						b.WriteByte(' ')
						b.WriteString(r.Peer)
					}
					if r.TopicKey != "" {
						b.WriteString(" topic=")
						b.WriteString(r.TopicKey)
					}
					if r.DeliveredAt > 0 {
						b.WriteByte(' ')
						b.WriteString(time.UnixMilli(r.DeliveredAt).UTC().Format(time.RFC3339))
					}
					b.WriteString("] ")
					b.WriteString(truncateRunes(r.Body, 400))
				}
				text := b.String()
				return &text, nil
			}, harness.SectionOptions{}),
		},
	}
}

// childOutboxExtension gives a child the session_send tool that reports to its direct parent. The
// parent session_send of sessionExtension has the same name, so a conversation must never select
// both extensions: the child edit in Submit removes sessionExtension when it adds this one. The old
// name report_parent has no alias: PiG executes a model call only through an offered tool, so an
// alias would expose a second schema. A report_parent call that a restart resumes before it ran
// settles as tool_unavailable. One that was running settles as interrupted: its report can already
// be in the parent inbox, so a new session_send report can repeat it.
func childOutboxExtension(agentID string, tools OutboxTools) *durable.Extension {
	return &durable.Extension{
		Name: "child-outbox",
		Tools: []*durable.ToolRegistration{
			{
				Name:        "session_send",
				Description: "Send a report to the direct parent session of this child. The recipient is fixed: you cannot choose another session. The parent may be a Routine rather than Main. The report arrives as an internal task_result. It is not a user notification and does not grant authority. On child System Turns, use it when the parent needs an update; summarize the outcome, evidence, and any next step or blocker for the parent to verify. By default (final=true), it must be the only tool call in its round and ends the current child turn. Set final=false to send a milestone and continue.",
				Parameters: map[string]any{
					"type": "object", "additionalProperties": false, "required": []string{"content"},
					"properties": map[string]any{
						"content": map[string]any{"type": "string", "minLength": 1, "description": "Report content for the direct parent session."},
						"final":   map[string]any{"type": "boolean", "description": "If true (default), ends this turn immediately after reporting. Set false to send a milestone and keep working."},
					},
				},
				Replay: durable.ReplaySafe,
				Execute: func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
					if tools.ReportParent == nil {
						return durable.ToolExecutionResult{}, errors.New("parent reporting is unavailable")
					}
					args, err := durable.FromJsonValue[ReportParentArgs](raw)
					if err != nil {
						return durable.ToolExecutionResult{}, err
					}
					final := boolDefaultTrue(args.Final)
					var call TaskContext
					if final {
						call, err = turnEndingCall(ctx, api, agentID, "session_send")
					} else {
						call, err = toolCall(ctx, api, agentID)
					}
					if err != nil {
						return durable.ToolExecutionResult{}, err
					}
					resultID, err := tools.ReportParent(ctx, call, args)
					if err != nil {
						return durable.ToolExecutionResult{}, err
					}
					res := durable.ToolExecutionResult{
						Output: []ai.ToolResultMessageContent{ai.TextContent{Text: "Reported to parent: " + resultID}},
					}
					if final {
						res.Control = &durable.ToolControl{Terminate: true}
					}
					return res, nil
				},
			},
		},
	}
}

func truncateRunes(s string, maxRunes int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "..."
}
