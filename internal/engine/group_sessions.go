package engine

import (
	"context"
	"encoding/json/v2"
	"fmt"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
)

// GroupSessionArgs exposes only parent-owned work in the current audience.
type GroupSessionArgs struct {
	Action  string `json:"action"`
	ID      string `json:"id,omitempty"`
	Title   string `json:"title,omitempty"`
	Message string `json:"message,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

type GroupSessionResult struct {
	Session  *SessionRef  `json:"session,omitempty"`
	Sessions []SessionRef `json:"sessions,omitempty"`
	Page     *SessionPage `json:"page,omitempty"`
}

func (a *Agents) groupSessionTool(ref Ref) *durable.ToolRegistration {
	return &durable.ToolRegistration{Name: "group_session", Description: "Start, list, read, send to or stop a direct child of this conversation in the same bot/chat/topic audience. Children inherit group-only resources and the initiating sender's current authorization. No private sessions, cross-bot work, projects or model overrides. Children cannot delegate. Return published resource paths to the parent for Telegram delivery.", Replay: durable.ReplaySafe, Parameters: map[string]any{"type": "object", "required": []string{"action"}, "additionalProperties": false, "properties": map[string]any{
		"action": map[string]any{"type": "string", "enum": []string{"start", "list", "read", "send", "stop"}},
		"id":     map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string", "enum": []string{"queue", "steer"}},
	}}, Execute: func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
		args, err := durable.FromJsonValue[GroupSessionArgs](raw)
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		source, err := taskSource(ctx, api)
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		caller := TaskContext{AgentID: ref.AgentID, Scope: ref.Scope, ConversationID: int64(api.ConversationId()), SourceRequestID: source, Key: fmt.Sprintf("%s/%s/%d", ref.AgentID, ref.Scope, api.TaskId())}
		result, err := a.config.Tasks.GroupSession(ctx, caller, args)
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		encoded, err := json.Marshal(result)
		return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: string(encoded)}}}, err
	}}
}
