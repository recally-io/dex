package engine

import (
	"context"
	"encoding/json/v2"
	"errors"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
)

// TelegramRequest selects a stored delivery, not an arbitrary peer or remote message ID.
type TelegramRequest struct {
	Action     string `json:"action"`
	DeliveryID string `json:"deliveryId,omitempty"`
	Index      int    `json:"index,omitzero"`
	Text       string `json:"text,omitempty"`
	Emoji      string `json:"emoji,omitempty"`
}
type TelegramDelivery struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Messages int    `json:"messages"`
}
type TelegramResult struct {
	MessageKind string             `json:"messageKind,omitempty"`
	DeliveryID  string             `json:"deliveryId,omitempty"`
	Status      string             `json:"status"`
	Text        string             `json:"text,omitempty"`
	MessageIDs  []string           `json:"messageIds,omitempty"`
	Deliveries  []TelegramDelivery `json:"deliveries,omitempty"`
}

func telegramExtension(agentID string, call func(context.Context, TaskContext, TelegramRequest) (TelegramResult, error)) *durable.Extension {
	return &durable.Extension{Name: "telegram", Tools: []*durable.ToolRegistration{{
		Name: "telegram_message", Description: "For a Telegram Main input only: list this peer's confirmed bot deliveries; edit text/caption, delete, react, copy or forward a selected stored delivery back to this same peer. Text edits accept Markdown; media caption edits use plain text. Speak uses explicitly configured TTS and queues native audio. Always keep its readable text in your final answer. Do not repeat a failed speech request without a new user instruction. No arbitrary peer/message IDs are accepted.",
		Replay: durable.ReplaySafe,
		Parameters: map[string]any{"type": "object", "required": []string{"action"}, "additionalProperties": false, "properties": map[string]any{
			"action":     map[string]any{"type": "string", "enum": []string{"list", "edit", "edit_caption", "delete", "react", "copy", "forward", "speak"}},
			"deliveryId": map[string]any{"type": "string"}, "index": map[string]any{"type": "integer", "minimum": 0}, "text": map[string]any{"type": "string"}, "emoji": map[string]any{"type": "string"},
		}},
		Execute: func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
			if call == nil {
				return durable.ToolExecutionResult{}, errors.New("telegram actions are unavailable")
			}
			args, err := durable.FromJsonValue[TelegramRequest](raw)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			caller, err := toolCall(ctx, api, agentID)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			result, err := call(ctx, caller, args)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			body, err := json.Marshal(result)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: string(body)}}}, nil
		},
	}}}
}
