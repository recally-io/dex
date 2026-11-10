package engine

import (
	"context"
	"encoding/json/v2"
	"errors"

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

func dreamMessageExtension(agentID string, read func(context.Context, TaskContext, DreamMessageRequest) (DreamMessagePage, error)) *durable.Extension {
	return &durable.Extension{Name: "dream-message", Tools: []*durable.ToolRegistration{{
		Name: "read_dream_message", Description: "Read a page of the original user message or answer for a record in this dream. Offset is in Unicode characters; use nextOffset while hasMore is true.", Replay: durable.ReplaySafe,
		Parameters: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"messageId", "field", "offset"}, "properties": map[string]any{
			"messageId": map[string]any{"type": "string", "minLength": 1}, "field": map[string]any{"type": "string", "enum": []string{"user", "answer"}}, "offset": map[string]any{"type": "integer", "minimum": 0},
		}},
		Execute: func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
			if read == nil {
				return durable.ToolExecutionResult{}, errors.New("dream message service unavailable")
			}
			request, err := taskSource(ctx, api)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			args, err := durable.FromJsonValue[DreamMessageRequest](raw)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			page, err := read(ctx, TaskContext{AgentID: agentID, ConversationID: int64(api.ConversationId()), SourceRequestID: request}, args)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			data, err := json.Marshal(page)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: string(data)}}}, nil
		},
	}}}
}
