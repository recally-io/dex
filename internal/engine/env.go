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

func codingTools(environment map[string]string, inheritEnv bool, bashTimeout time.Duration) *durable.Extension {
	timeoutSeconds := cmp.Or(bashTimeout, 120*time.Second).Seconds()
	extension := tools.CreateCodingTools(nil)
	for i, tool := range extension.Tools {
		if tool.Name == "bash" {
			bash := tools.CreateBashTool(&tools.BashToolOptions{Prepare: func(_ context.Context, execution *tools.BashExecution, _ durable.ToolExecutionApi) error {
				execution.InheritEnv = inheritEnv
				execution.Env = maps.Clone(environment)
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
