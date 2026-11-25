package chat

import (
	"context"
	"errors"

	"github.com/recally-io/dex/internal/engine"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// CheckReport is a routine's explicit result for its current input.
type CheckReport struct {
	Outcome   string `json:"outcome"`
	Summary   string `json:"summary"`
	ChangeKey string `json:"changeKey,omitempty"`
}

const routineCheckRules = "Before this automation run ends, call automation_report once with changed, unchanged, or failed, a short summary, and an optional stable change key. Report unchanged only after the required checks succeeded and found no change. Missing reports and execution errors fail the run. A repeated change key marks a duplicate; it does not undo or prevent actions."
const BoundRoutineCheckSuffix = "\n\nRoutine reporting instructions (for this input only): " + routineCheckRules

// Keep the existing section and extension name for dedicated Routine sessions.
func RoutineCheckInstructions() *engine.Extension {
	return &engine.Extension{Name: "automation-check", Sections: []*durable.PromptSection{
		harness.Section("automation-check", func(context.Context, durable.PromptInput) (*string, error) {
			return new(routineCheckRules), nil
		}, harness.SectionOptions{Tag: new(false)}),
	}}
}

func CheckExtension(agentID string, report func(context.Context, engine.TaskContext, CheckReport) error) *engine.Extension {
	schema := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"outcome", "summary"}, "properties": map[string]any{
		"outcome":   map[string]any{"type": "string", "enum": []string{"changed", "unchanged", "failed"}},
		"summary":   map[string]any{"type": "string", "minLength": 1, "maxLength": 1000},
		"changeKey": map[string]any{"type": "string", "maxLength": 200},
	}}
	return &engine.Extension{Name: "routine-reporting", Tools: []*durable.ToolRegistration{{Name: "automation_report", Description: "Report the outcome of the current Routine run. This tool only works during a Routine run. Use a stable change key to identify the same change across runs.", Replay: durable.ReplaySafe, Parameters: schema,
		Execute: engine.BindTool(agentID, func(ctx context.Context, raw any, invocation engine.ToolCall) (durable.ToolExecutionResult, error) {
			if report == nil {
				return durable.ToolExecutionResult{}, errors.New("automation_report reporting is unavailable")
			}
			args, err := durable.FromJsonValue[CheckReport](raw)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			id, err := invocation.Source(ctx)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			if err = report(ctx, engine.TaskContext{AgentID: agentID, ConversationID: invocation.ConversationID(), SourceRequestID: id}, args); err != nil {
				return durable.ToolExecutionResult{}, err
			}
			return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: "Automation report saved."}}}, nil
		})}}}
}
