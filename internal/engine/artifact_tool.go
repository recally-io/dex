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

func artifactExtension(agentID, workspace string, deliver func(context.Context, TaskContext, string) (ArtifactInfo, error)) *durable.Extension {
	rules := fmt.Sprintf("To deliver a report or file to the user in Dex, copy it into your workspace at %s, then call deliver_file with its workspace-relative path. This also applies when the project directory is elsewhere. Do the copy yourself; do not ask the user to move files. Each delivery is a snapshot and will not follow later source changes. Never deliver secrets.", workspace)
	return &durable.Extension{Name: "artifact-delivery", Sections: []*durable.PromptSection{
		harness.Section("artifact-delivery", func(context.Context, durable.PromptInput) (*string, error) { return &rules, nil }, harness.SectionOptions{Tag: new(false)}),
	}, Tools: []*durable.ToolRegistration{{
		Name: "deliver_file", Description: "Deliver a workspace file to the user as an immutable snapshot. The path is relative to the agent workspace, not the project directory.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "minLength": 1}}, "required": []string{"path"}, "additionalProperties": false},
		Replay:     durable.ReplaySafe,
		Execute: func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
			if deliver == nil {
				return durable.ToolExecutionResult{}, errors.New("artifact service is unavailable")
			}
			args, err := durable.FromJsonValue[struct {
				Path string `json:"path"`
			}](raw)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			requestID, err := taskSource(ctx, api)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			result, err := deliver(ctx, TaskContext{AgentID: agentID, ConversationID: int64(api.ConversationId()), SourceRequestID: requestID, Key: fmt.Sprintf("%s/%d", agentID, api.TaskId())}, args.Path)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			data, err := json.Marshal(result)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: string(data)}}}, nil
		},
	}}}
}
