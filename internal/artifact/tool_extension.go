package artifact

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"

	"github.com/recally-io/dex/internal/engine"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

type Delivery struct {
	ChannelDelivery string `json:"channelDelivery,omitempty"`
	Name            string `json:"name"`
	Size            int64  `json:"size"`
	URL             string `json:"url"`
}

// Extension has deliver_file. Its rules name the workspace of the session's environment;
// when that environment is unavailable, they omit the path, and deliver_file reports the reason.
func Extension(runtime engine.ExtensionRuntime, deliver func(context.Context, engine.TaskContext, string) (Delivery, error)) *engine.Extension {
	const format = "To deliver a report or file to the user in Dex, copy it into your workspace at %s, then call deliver_file with its workspace-relative path. This also applies when the project directory is elsewhere. Do the copy yourself; do not ask the user to move files. Files go into this bot’s artifacts directory under their actual names. Existing names are conflicts; choose a different file name rather than overwrite. Share the returned URL, including when reporting to a parent in another bot. Never deliver secrets."
	return &engine.Extension{Name: "artifact-delivery", Sections: []*durable.PromptSection{
		harness.Section("artifact-delivery", func(ctx context.Context, input durable.PromptInput) (*string, error) {
			workspace := "the workspace of this session"
			if s, err := runtime.Environment(ctx, int64(input.ConversationId)); err == nil {
				workspace = s.Handle.Workspace()
			}
			rules := fmt.Sprintf(format, workspace)
			return &rules, nil
		}, harness.SectionOptions{Tag: new(false)}),
	}, Tools: []*durable.ToolRegistration{{
		Name: "deliver_file", Description: "Copy a workspace file into this bot's artifacts directory and return its URL. For a Telegram Main input, also queue the file for the original recipient. Existing names are conflicts.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string", "minLength": 1}}, "required": []string{"path"}, "additionalProperties": false},
		Replay:     durable.ReplaySafe,
		Execute: engine.BindTool(runtime.BotID, func(ctx context.Context, raw any, invocation engine.ToolCall) (durable.ToolExecutionResult, error) {
			if deliver == nil {
				return durable.ToolExecutionResult{}, errors.New("artifact service is unavailable")
			}
			args, err := durable.FromJsonValue[struct {
				Path string `json:"path"`
			}](raw)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			caller, err := invocation.Context(ctx)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			result, err := deliver(ctx, caller, args.Path)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			data, err := json.Marshal(result)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: string(data)}}}, nil
		}),
	}}}
}
