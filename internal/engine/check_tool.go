package engine

import (
	"context"
	"errors"
	"slices"

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

const (
	checkExtensionName      = "automation-check"
	boundCheckExtensionName = "bound-automation-check"
)

func checkExtension(agentID string, report func(context.Context, TaskContext, CheckReport) error, store durable.Storage) *durable.Extension {
	rules := "Before this automation run ends, call report_check once with changed, unchanged, or failed, a short summary, and an optional stable change key. Report unchanged only after the required checks succeeded and found no change. Missing reports and execution errors fail the run. A repeated change key marks a duplicate; it does not undo or prevent actions."
	schema := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"outcome", "summary"}, "properties": map[string]any{
		"outcome":   map[string]any{"type": "string", "enum": []string{"changed", "unchanged", "failed"}},
		"summary":   map[string]any{"type": "string", "minLength": 1, "maxLength": 1000},
		"changeKey": map[string]any{"type": "string", "maxLength": 200},
	}}
	name := checkExtensionName
	if store != nil {
		name = boundCheckExtensionName
	}
	extension := &durable.Extension{Name: name, Sections: []*durable.PromptSection{
		harness.Section(checkExtensionName, func(ctx context.Context, input durable.PromptInput) (*string, error) {
			if store == nil {
				return &rules, nil
			}
			active, err := routineRunInput(ctx, store, input.Read, input.ConversationId)
			if err != nil || !active {
				return nil, err
			}
			return &rules, nil
		}, harness.SectionOptions{Tag: new(false)}),
	}, Tools: []*durable.ToolRegistration{{Name: "report_check", Description: "Report the outcome of this automation run. Use a stable change key to identify the same change across runs.", Replay: durable.ReplaySafe, Parameters: schema,
		Execute: func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
			if report == nil {
				return durable.ToolExecutionResult{}, errors.New("report_check reporting is unavailable")
			}
			args, err := durable.FromJsonValue[CheckReport](raw)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			id, err := taskSource(ctx, api)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			if err = report(ctx, TaskContext{AgentID: agentID, ConversationID: int64(api.ConversationId()), SourceRequestID: id}, args); err != nil {
				return durable.ToolExecutionResult{}, err
			}
			return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: "Automation report saved."}}}, nil
		}}}}
	if store != nil {
		// PiG persists the extension on the conversation. Hide its tool from the model request
		// for other inputs without changing the stored transcript or the target's capabilities.
		// The actual reporting operation still verifies its source receipt.
		extension.Hooks = []durable.HookRegistration{harness.Hook(harness.GenerationTask, &harness.GenerationHooks{
			BeforeRequest: func(ctx context.Context, request harness.GenerationRequest, api harness.HookApi) (*harness.GenerationRequest, error) {
				if active, _ := routineRunInput(ctx, store, api, api.ConversationId()); active {
					return nil, nil
				}
				messages := slices.Clone(request.Messages)
				for i, message := range messages {
					if system, ok := message.(ai.SystemMessage); ok && slices.ContainsFunc(system.ToolsAdded, isReportCheck) {
						system.ToolsAdded = slices.DeleteFunc(slices.Clone(system.ToolsAdded), isReportCheck)
						messages[i] = system
					}
				}
				return &harness.GenerationRequest{Messages: messages}, nil
			},
		})}
	}
	return extension
}

func isReportCheck(tool ai.ToolSchema) bool { return tool.Name == "report_check" }

// The first placed input keeps its identity on retry or when the owner steers the run.
func routineRunInput(ctx context.Context, store durable.Storage, reader durable.DocumentReader, conversationID durable.ConversationId) (bool, error) {
	live, err := durable.Snapshot(ctx, reader, harness.LiveDoc, conversationID)
	if err != nil || live == nil || live.Run == nil || len(live.Run.Inputs) == 0 {
		return false, err
	}
	source, err := store.Submission(ctx, live.Run.Inputs[0])
	if err != nil || source == nil || source.Entry == nil || source.ConversationId != conversationID {
		return false, err
	}
	entry, err := store.Entry(ctx, *source.Entry)
	if err != nil || entry == nil || entry.Entry.Kind != "dex.user" {
		return false, err
	}
	return metadata(entry.Entry.Data).Origin == "automation", nil
}
