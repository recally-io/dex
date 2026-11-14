package engine

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
)

func (a *Agents) groupDeliveryTool(ref Ref, root *os.Root) *durable.ToolRegistration {
	return &durable.ToolRegistration{Name: "group_deliver", Description: "Queue a file explicitly published in this bot/chat/topic audience for this source's Telegram reply. This cannot read private workspace or bot-wide artifacts. Queued is not remote confirmation.", Replay: durable.ReplaySafe, Parameters: map[string]any{"type": "object", "required": []string{"path"}, "additionalProperties": false, "properties": map[string]any{"path": map[string]any{"type": "string", "minLength": 1}}}, Execute: func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
		args, err := durable.FromJsonValue[struct {
			Path string `json:"path"`
		}](raw)
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		file, err := openGroupResource(root, args.Path)
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		source, err := taskSource(ctx, api)
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		caller := TaskContext{AgentID: ref.AgentID, Scope: ref.Scope, ConversationID: int64(api.ConversationId()), SourceRequestID: source, Key: fmt.Sprintf("%s/%s/%d", ref.AgentID, ref.Scope, api.TaskId())}
		name := filepath.Base(args.Path)
		if err := a.config.Tasks.DeliverGroupFile(ctx, caller, name, file); err != nil {
			return durable.ToolExecutionResult{}, err
		}
		result, err := json.Marshal(ArtifactInfo{Name: name, Size: info.Size(), ChannelDelivery: "queued"})
		return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: string(result)}}}, err
	}}
}
