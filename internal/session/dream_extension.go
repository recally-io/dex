package session

import (
	"context"
	"encoding/json/v2"
	"errors"

	"github.com/recally-io/dex/internal/engine"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
)

// DreamMessageRequest selects one field and a rune offset in a dream record.
type DreamMessageRequest struct {
	MessageID string `json:"messageId"`
	Field     string `json:"field"`
	Offset    int    `json:"offset"`
}

// DreamMessagePage contains a bounded part of the original message.
type DreamMessagePage struct {
	Text       string `json:"text"`
	NextOffset int    `json:"nextOffset"`
	HasMore    bool   `json:"hasMore"`
}

func DreamMessageExtension(agentID string, read func(context.Context, engine.TaskContext, DreamMessageRequest) (DreamMessagePage, error)) *engine.Extension {
	return &engine.Extension{Name: "dream-message", Tools: []*durable.ToolRegistration{{
		Name: "memory_source_read", Description: "Read a page of the original user message or answer for a source supplied in this memory consolidation run. Offset is in Unicode characters; use nextOffset while hasMore is true.", Replay: durable.ReplaySafe,
		Parameters: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"messageId", "field", "offset"}, "properties": map[string]any{
			"messageId": map[string]any{"type": "string", "minLength": 1}, "field": map[string]any{"type": "string", "enum": []string{"user", "answer"}}, "offset": map[string]any{"type": "integer", "minimum": 0},
		}},
		Execute: engine.BindTool(agentID, func(ctx context.Context, raw any, invocation engine.ToolCall) (durable.ToolExecutionResult, error) {
			if read == nil {
				return durable.ToolExecutionResult{}, errors.New("memory source service is unavailable")
			}
			request, err := invocation.Source(ctx)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			args, err := durable.FromJsonValue[DreamMessageRequest](raw)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			page, err := read(ctx, engine.TaskContext{AgentID: agentID, ConversationID: invocation.ConversationID(), SourceRequestID: request}, args)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			data, err := json.Marshal(page)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: string(data)}}}, nil
		}),
	}}}
}
