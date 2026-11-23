package engine

import (
	"cmp"
	"context"
	"maps"
	"time"

	"github.com/MichaelKinsy/PiG/durable"
	durableenv "github.com/MichaelKinsy/PiG/durable/env"
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
				execution.Env = maps.Clone(s.Vars)
				if s.Files != nil {
					if cache := s.Files.skillCache(execution.Cwd); cache != "" {
						if err := api.Env().CreateDir(ctx, cache, &durableenv.CreateDirOptions{Recursive: new(true)}); err != nil {
							return err
						}
						if execution.Env == nil {
							execution.Env = map[string]string{}
						}
						execution.Env["DEX_SKILL_CACHE"] = cache
					}
				}
				return nil
			}})
			bash.Description += " The default timeout is 120 seconds. Set timeout for longer foreground work, or use background_start for long work."
			if properties, ok := bash.Parameters["properties"].(map[string]any); ok {
				if timeout, ok := properties["timeout"].(map[string]any); ok {
					timeout["description"] = "Timeout in seconds. The default is 120 seconds."
				}
			}
			bash.PrepareArguments = bashDefaultTimeout(timeoutSeconds)
			bash.Parameters = maps.Clone(bash.Parameters)
			properties, _ := bash.Parameters["properties"].(map[string]any)
			props := maps.Clone(properties)
			props["cwd"] = map[string]any{"type": "string", "description": "Optional working directory. For a managed skill directory, dex sets DEX_SKILL_CACHE."}
			bash.Parameters["properties"] = props
			execute := bash.Execute
			bash.Execute = func(ctx context.Context, raw any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
				args, err := durable.FromJsonValue[map[string]any](raw)
				if err != nil {
					return durable.ToolExecutionResult{}, err
				}
				if cwd, _ := args["cwd"].(string); cwd != "" && api.Env() != nil {
					env := api.Env()
					absolute, err := env.AbsolutePath(ctx, cwd)
					if err != nil {
						return durable.ToolExecutionResult{}, err
					}
					api = memoryToolAPI{ToolExecutionApi: api, env: bashCwdEnv{env, absolute}}
				}
				return execute(ctx, raw, api)
			}
			extension.Tools[i] = bash
		}
	}
	return extension
}

type bashCwdEnv struct {
	durableenv.ExecutionEnv
	cwd string
}

func (e bashCwdEnv) Cwd() string { return e.cwd }
