package engine

import (
	"cmp"
	"context"
	"maps"
	"time"

	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/tools"
	"github.com/recally-io/dex/internal/execenv"
	"github.com/recally-io/dex/internal/vault"
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

type bashEnvKey struct{}

// codingTools has the command and file tools. session returns the environment of the calling
// session; the Harness Env hook resolves the same environment for the file tools.
func codingTools(session func(context.Context, durable.ConversationId) (sessionEnv, error), bashTimeout time.Duration, secrets vault.Snapshot) *durable.Extension {
	timeoutSeconds := cmp.Or(bashTimeout, 120*time.Second).Seconds()
	extension := tools.CreateCodingTools(nil)
	for i, tool := range extension.Tools {
		if tool.Name == "write" || tool.Name == "edit" {
			protectSkillTool(tool)
		}
		if tool.Name == "bash" {
			bash := tools.CreateBashTool(&tools.BashToolOptions{Prepare: func(ctx context.Context, execution *tools.BashExecution, api durable.ToolExecutionApi) error {
				s, err := session(ctx, api.ConversationId())
				if err != nil {
					return err
				}
				execution.InheritEnv = s.Inherit
				execution.Env = make(map[string]string, len(s.Vars)+1)
				maps.Copy(execution.Env, s.Vars)
				execution.Env["DEX_SKILL_CACHE"] = execenv.JoinTargetPath(s.Handle.Workspace(), ".dex", "cache", "skills")
				extra, _ := ctx.Value(bashEnvKey{}).(map[string]string)
				maps.Copy(execution.Env, extra)
				return nil
			}})
			bash.Description += " The default timeout is 120 seconds. Set timeout for longer foreground work, or use monitor_set_command for long work."
			if properties, ok := bash.Parameters["properties"].(map[string]any); ok {
				if timeout, ok := properties["timeout"].(map[string]any); ok {
					timeout["description"] = "Timeout in seconds. The default is 120 seconds."
				}
				properties["env"] = commandEnvSchema("Environment variables for this call only. Use vault:NAME for secrets.")
			}
			bash.PrepareArguments = bashDefaultTimeout(timeoutSeconds)
			execute := bash.Execute
			bash.Execute = func(ctx context.Context, args any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
				// Pass only literal bash arguments to PiG. The saved arguments keep references.
				input, err := durable.FromJsonValue[map[string]any](args)
				if err != nil {
					return durable.ToolExecutionResult{}, err
				}
				if raw, ok := input["env"]; ok {
					extra, err := durable.FromJsonValue[map[string]string](raw)
					if err != nil {
						return durable.ToolExecutionResult{}, err
					}
					resolved, err := secrets.Resolve(extra, false)
					if err != nil {
						return durable.ToolExecutionResult{}, err
					}
					input = maps.Clone(input)
					delete(input, "env")
					ctx = context.WithValue(ctx, bashEnvKey{}, resolved)
				}
				return execute(ctx, input, api)
			}
			extension.Tools[i] = bash
		}
	}
	return extension
}
