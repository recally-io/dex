package bot

import (
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"

	"github.com/recally-io/dex/internal/engine"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/harness"
)

// ChannelTools connects owner tools to the control plane. The model can propose
// a change, but only the owner surface can apply it.
type ChannelTools struct {
	Read    func(context.Context, engine.TaskContext, string, string) (any, error)
	Request func(context.Context, engine.TaskContext, string, string, string) (any, error)
}

const channelChangeTool = "channel_request_change"

func IsChannelTool(name string) bool {
	switch name {
	case "channel_status", "channel_pairing", "channel_notification_targets", channelChangeTool:
		return true
	}
	return false
}

func ChannelExtensions(botID string, h ChannelTools) []*engine.Extension {
	bot := map[string]any{"type": "string", "description": "Bot ID from bot_list. Omit to use the current bot."}
	read := &engine.Extension{Name: "channel-read", Hooks: []durable.HookRegistration{channelValidationHook(botID, false)}}
	for _, view := range []struct{ name, description string }{
		{"channel_status", "Show channel runtime status for a bot."},
		{"channel_pairing", "Show which channels have a paired owner. This tool does not give pairing codes."},
		{"channel_notification_targets", "List authorized notification destinations and the primary destination for a bot."},
	} {
		read.Tools = append(read.Tools, channelTool(botID, view.name, view.description,
			map[string]any{"bot": bot}, nil, false,
			func(ctx context.Context, call engine.TaskContext, args map[string]string) (any, error) {
				if h.Read == nil {
					return nil, errChannelToolInvalid
				}
				return h.Read(ctx, call, view.name, args["bot"])
			}))
	}
	write := &engine.Extension{Name: "channel-manage", Hooks: []durable.HookRegistration{channelValidationHook(botID, true)}, Tools: []*durable.ToolRegistration{
		channelTool(botID, channelChangeTool, "Request owner confirmation. Give the owner the returned link. Do not open it or submit it yourself. Use Channels settings for credentials, pairing codes, or QR login.", map[string]any{
			"bot":       bot,
			"operation": map[string]any{"type": "string", "enum": []string{"revoke_pairing", "disconnect", "set_notification_target", "clear_notification_target"}},
			"target":    map[string]any{"type": "string", "description": "For pairing or disconnect, use telegram, feishu or wechat. To set a destination, use its ID from channel_notification_targets. To clear the destination, omit target."},
		}, []string{"operation"}, true, func(ctx context.Context, call engine.TaskContext, args map[string]string) (any, error) {
			if h.Request == nil {
				return nil, errChannelToolInvalid
			}
			return h.Request(ctx, call, args["bot"], args["operation"], args["target"])
		}),
	}}
	return []*engine.Extension{read, write}
}

var errChannelToolInvalid = errors.New("invalid channel tool arguments")

// PiG can include the complete input in a validation error. Do not return it
// to the model or store it in the tool result.
func channelValidationHook(botID string, write bool) durable.HookRegistration {
	return harness.Hook(harness.ToolTask, &harness.ToolHooks{AfterTool: func(ctx context.Context, call harness.ToolHookCall, result durable.ToolExecutionResult, _ harness.HookApi) (*durable.ToolExecutionResult, error) {
		if !IsChannelTool(call.Name) || (call.Name == channelChangeTool) != write {
			return nil, nil
		}
		for _, d := range result.Diagnostics {
			if d.Code == "invalid_arguments" {
				if write {
					slog.InfoContext(ctx, "channel tool mutation", "outcome", "denied", "caller", botID)
				}
				return new(harness.HarnessError(d.Code, errChannelToolInvalid.Error())), nil
			}
		}
		return nil, nil
	}})
}

func channelTool(botID, name, description string, fields map[string]any, required []string, write bool, invoke func(context.Context, engine.TaskContext, map[string]string) (any, error)) *durable.ToolRegistration {
	replay := durable.ReplaySafe
	if write {
		replay = durable.ReplayUnsafe
	}
	return &durable.ToolRegistration{Name: name, Description: description, Parameters: engine.ObjectSchema(required, fields), Replay: replay,
		Callers: []durable.ToolCaller{durable.CallerModel},
		Execute: engine.BindTool(botID, func(ctx context.Context, raw any, invocation engine.ToolCall) (durable.ToolExecutionResult, error) {
			call, err := invocation.Context(ctx)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			args, err := durable.FromJsonValue[map[string]string](raw)
			if err != nil {
				return durable.ToolExecutionResult{}, errChannelToolInvalid
			}
			result, err := invoke(ctx, call, args)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			data, err := json.Marshal(result)
			if err != nil {
				return durable.ToolExecutionResult{}, err
			}
			return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: string(data)}}}, nil
		}),
	}
}
