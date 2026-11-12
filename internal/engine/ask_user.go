package engine

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"

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

// turnEndingCall returns the call context of a tool that ends the turn with Terminate. Native
// Terminate ends the turn only when every call of the round asks for it. The round's tool slots are
// saved before any tool starts, so a mixed round is visible here.
func turnEndingCall(ctx context.Context, api durable.ToolExecutionApi, agentID, tool string) (TaskContext, error) {
	live, err := durable.Snapshot[harness.LiveState](ctx, api, harness.LiveDoc, api.ConversationId())
	if err != nil {
		return TaskContext{}, err
	}
	if live == nil || len(live.Tools) != 1 {
		return TaskContext{}, fmt.Errorf("%s must be the only tool call", tool)
	}
	return toolCall(ctx, api, agentID)
}

// toolCall names the agent, the conversation, the source input, and the tool operation key of a call.
func toolCall(ctx context.Context, api durable.ToolExecutionApi, agentID string) (TaskContext, error) {
	requestID, err := taskSource(ctx, api)
	if err != nil {
		return TaskContext{}, err
	}
	return TaskContext{AgentID: agentID, ConversationID: int64(api.ConversationId()), SourceRequestID: requestID, Key: fmt.Sprintf("%s/%d", agentID, api.TaskId())}, nil
}

// askUserExtension adds the question tool. Only Main and Chat sessions have it. The question is
// saved in control; the engine only ends the turn.
func askUserExtension(agentID string, ask func(context.Context, TaskContext, AskUserArgs) (QuestionInfo, error)) *durable.Extension {
	return &durable.Extension{Name: "ask-user", Sections: []*durable.PromptSection{
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
		Execute: func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
			if ask == nil {
				return durable.ToolExecutionResult{}, errors.New("question service is unavailable")
			}
			args, err := durable.FromJsonValue[AskUserArgs](raw)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			call, err := turnEndingCall(ctx, api, agentID, "ask_user")
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
		},
	}}}
}
