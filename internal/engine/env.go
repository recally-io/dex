package engine

import (
	"cmp"
	"context"
	"maps"
	"time"

	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/tools"
)

// bashDefaultTimeout leaves validation and process cancellation to the native bash tool.
func bashDefaultTimeout(timeoutSeconds float64) func(any) (any, error) {
	return func(raw any) (any, error) {
		input, err := durable.FromJsonValue[map[string]any](raw)
		if err != nil {
			return nil, err
		}
		if input == nil {
			return raw, nil
		}
		if input["timeout"] == nil {
			input["timeout"] = timeoutSeconds
		}
		return input, nil
	}
}

// codingTools has the command and file tools. session returns the environment of the calling
// session; the Harness Env hook resolves the same environment for the file tools.
func codingTools(session func(context.Context, durable.ConversationId) (sessionEnv, error), bashTimeout time.Duration) *durable.Extension {
	timeoutSeconds := cmp.Or(bashTimeout, 120*time.Second).Seconds()
	extension := tools.CreateCodingTools(nil)
	for i, tool := range extension.Tools {
		if tool.Name == "bash" {
			bash := tools.CreateBashTool(&tools.BashToolOptions{Prepare: func(ctx context.Context, execution *tools.BashExecution, api durable.ToolExecutionApi) error {
				s, err := session(ctx, api.ConversationId())
				if err != nil {
					return err
				}
				execution.InheritEnv = s.Inherit
				execution.Env = maps.Clone(s.Vars)
				return nil
			}})
			bash.Description += " The default timeout is 120 seconds. Set timeout for longer foreground work, or use background_start for long work."
			if properties, ok := bash.Parameters["properties"].(map[string]any); ok {
				if timeout, ok := properties["timeout"].(map[string]any); ok {
					timeout["description"] = "Timeout in seconds. The default is 120 seconds."
				}
			}
			bash.PrepareArguments = bashDefaultTimeout(timeoutSeconds)
			extension.Tools[i] = bash
		}
	}
	return extension
}
