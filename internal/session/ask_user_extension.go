package session

import (
	"context"
	"encoding/json/v2"
	"errors"

	"github.com/recally-io/dex/internal/engine"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// AskUserArgs is the input of the ask_user tool.
type AskUserArgs struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

// QuestionInfo is a saved question. The tool returns it to the model.
type QuestionInfo struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Options []string `json:"options"`
}

const askUserRules = `Use ask_user when you need the user to decide between 2 to 4 options before you can go on. Call it alone, with no other tool call in the same message, and write no text after it: the call ends your turn. The user answers in a later input that starts with "Answer to:". Do not use ask_user to get approval for side effects.`

// AskUserExtension adds the question tool. Only Main and top-level Web sessions have it. The question is
// saved in control; the engine only ends the turn.
func AskUserExtension(agentID string, ask func(context.Context, engine.TaskContext, AskUserArgs) (QuestionInfo, error)) *engine.Extension {
	return &engine.Extension{Name: "ask-user", Sections: []*durable.PromptSection{
		harness.Section("ask-user", func(context.Context, durable.PromptInput) (*string, error) { return new(askUserRules), nil }, harness.SectionOptions{Tag: new(false)}),
	}, Tools: []*durable.ToolRegistration{{
		Name:        "ask_user",
		Description: "Ask the user one question with 2 to 4 options and end this turn. The user can pick an option or write another answer.",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"question", "options"},
			"properties": map[string]any{
				"question": map[string]any{"type": "string", "minLength": 1},
				"options":  map[string]any{"type": "array", "minItems": 2, "maxItems": 4, "items": map[string]any{"type": "string", "minLength": 1}},
			},
		},
		Replay: durable.ReplaySafe,
		Execute: engine.BindTool(agentID, func(ctx context.Context, raw any, invocation engine.ToolCall) (durable.ToolExecutionResult, error) {
			if ask == nil {
				return durable.ToolExecutionResult{}, errors.New("question service is unavailable")
			}
			args, err := durable.FromJsonValue[AskUserArgs](raw)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			call, err := invocation.TurnEnding(ctx, "ask_user")
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			question, err := ask(ctx, call, args)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			data, err := json.Marshal(question)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			return durable.ToolExecutionResult{
				Output:  []ai.ToolResultMessageContent{ai.TextContent{Text: "The question is sent to the user. Your turn ends now. " + string(data)}},
				Control: &durable.ToolControl{Terminate: true},
			}, nil
		}),
	}}}
}
