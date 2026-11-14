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
	rules := fmt.Sprintf("To deliver a report or file to the user in Dex, copy it into your workspace at %s, then call deliver_file with its workspace-relative path. This also applies when the project directory is elsewhere. Do the copy yourself; do not ask the user to move files. Files go into this bot’s artifacts directory under their actual names. Existing names are conflicts; choose a different file name rather than overwrite. Share the returned URL, including when reporting to a parent in another bot. Never deliver secrets.", workspace)
	return &durable.Extension{Name: "artifact-delivery", Sections: []*durable.PromptSection{
		harness.Section("artifact-delivery", func(context.Context, durable.PromptInput) (*string, error) { return &rules, nil }, harness.SectionOptions{Tag: new(false)}),
	}, Tools: []*durable.ToolRegistration{{
		Name: "deliver_file", Description: "Copy a workspace file into this bot’s artifacts directory. Return the file URL. Existing names are conflicts.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "minLength": 1}}, "required": []string{"path"}, "additionalProperties": false},
		Replay:     durable.ReplaySafe,
		Execute: func(ctx context.Context, raw any, _ durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
			if deliver == nil {
				return durable.ToolExecutionResult{}, errors.New("artifact service is unavailable")
			}
			args, err := durable.FromJsonValue[struct {
				Path string `json:"path"`
			}](raw)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			result, err := deliver(ctx, TaskContext{AgentID: agentID}, args.Path)
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
