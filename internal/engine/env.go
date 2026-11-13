package engine

import (
	"context"
	"maps"

	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/tools"
)

const defaultBashTimeoutSeconds = 120.0

// bashDefaultTimeout leaves validation and process cancellation to the native bash tool.
func bashDefaultTimeout(raw any) (any, error) {
	input, err := durable.FromJsonValue[map[string]any](raw)
	if err != nil {
		return nil, err
	}
	if input == nil {
		return raw, nil
	}
	if input["timeout"] == nil {
		input["timeout"] = defaultBashTimeoutSeconds
	}
	return input, nil
}

func codingTools(environment map[string]string) *durable.Extension {
	extension := tools.CreateCodingTools(nil)
	for i, tool := range extension.Tools {
		if tool.Name == "bash" {
			bash := tools.CreateBashTool(&tools.BashToolOptions{Prepare: func(_ context.Context, execution *tools.BashExecution, _ durable.ToolExecutionApi) error {
				execution.InheritEnv = false
				execution.Env = maps.Clone(environment)
				return nil
			}})
			bash.Description += " The default timeout is 120 seconds. Set timeout for longer foreground work, or use background_start for long work."
			if properties, ok := bash.Parameters["properties"].(map[string]any); ok {
				if timeout, ok := properties["timeout"].(map[string]any); ok {
					timeout["description"] = "Timeout in seconds. The default is 120 seconds."
				}
			}
			bash.PrepareArguments = bashDefaultTimeout
			extension.Tools[i] = bash
		}
	}
	return extension
}
