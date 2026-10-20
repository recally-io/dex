package engine

import (
	"context"
	"maps"

	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/tools"
)

func codingTools(environment map[string]string) *durable.Extension {
	extension := tools.CreateCodingTools(nil)
	for i, tool := range extension.Tools {
		if tool.Name == "bash" {
			extension.Tools[i] = tools.CreateBashTool(&tools.BashToolOptions{Prepare: func(_ context.Context, execution *tools.BashExecution, _ durable.ToolExecutionApi) error {
				execution.InheritEnv = false
				execution.Env = maps.Clone(environment)
				return nil
			}})
		}
	}
	return extension
}
