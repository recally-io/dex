package engine

import (
	"context"
	"encoding/json/v2"
	"fmt"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
)

func (a *Agents) groupQuestionTool(ref Ref) *durable.ToolRegistration {
	return &durable.ToolRegistration{Name: "group_question", Description: "Ask the initiating paired sender one public question with 2 to 4 options and end this turn. Call this tool alone. This cannot authorize owner actions, private resources or privileges.", Replay: durable.ReplaySafe, Parameters: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"question", "options"}, "properties": map[string]any{"question": map[string]any{"type": "string", "minLength": 1}, "options": map[string]any{"type": "array", "minItems": 2, "maxItems": 4, "items": map[string]any{"type": "string", "minLength": 1}}}}, Execute: func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
		args, err := durable.FromJsonValue[AskUserArgs](raw)
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		caller, err := turnEndingCall(ctx, api, ref.AgentID, "group_question")
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		caller.Scope = ref.Scope
		caller.Key = fmt.Sprintf("%s/%s/%d", ref.AgentID, ref.Scope, api.TaskId())
		question, err := a.config.Tasks.GroupQuestion(ctx, caller, args)
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		encoded, err := json.Marshal(question)
		return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: string(encoded)}}, Control: &durable.ToolControl{Terminate: true}}, err
	}}
}
