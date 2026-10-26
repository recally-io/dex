package engine

import (
	"context"
	"errors"

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

func checkExtension(agentID string, report func(context.Context, TaskContext, CheckReport) error) *durable.Extension {
	rules := "Before this automation run ends, call report_check once with changed, unchanged, or failed, a short summary, and an optional stable change key. Report unchanged only after the required checks succeeded and found no change. Missing reports and execution errors fail the run. A repeated change key marks a duplicate; it does not undo or prevent actions."
	return &durable.Extension{
		Name: "automation-check",
		Sections: []*durable.PromptSection{
			harness.Section("automation-check", func(context.Context, durable.PromptInput) (*string, error) {
				return &rules, nil
			}, harness.SectionOptions{Tag: new(false)}),
		},
		Tools: []*durable.ToolRegistration{{
			Name: "report_check", Description: "Report the outcome of this automation run. Use a stable change key to identify the same change across runs.", Replay: durable.ReplaySafe,
			Parameters: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"outcome", "summary"}, "properties": map[string]any{
				"outcome":   map[string]any{"type": "string", "enum": []string{"changed", "unchanged", "failed"}},
				"summary":   map[string]any{"type": "string", "minLength": 1, "maxLength": 1000},
				"changeKey": map[string]any{"type": "string", "maxLength": 200},
			}},
			Execute: func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
				if report == nil {
					return durable.ToolExecutionResult{}, errors.New("automation reporting is unavailable")
				}
				args, err := durable.FromJsonValue[CheckReport](raw)
				if err != nil {
					return durable.ToolExecutionResult{}, err
				}
				id, err := taskSource(ctx, api)
				if err != nil {
					return durable.ToolExecutionResult{}, err
				}
				err = report(ctx, TaskContext{AgentID: agentID, ConversationID: int64(api.ConversationId()), SourceRequestID: id}, args)
				if err != nil {
					return durable.ToolExecutionResult{}, err
				}
				return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: "Automation report saved."}}}, nil
			},
		}},
	}
}
