package engine

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
)

func (a *Agents) mcpServerTools(agentID string, loaded *loadedExtensions) []*durable.ToolRegistration {
	store := a.config.MCPServers
	if store == nil {
		return nil
	}
	type arguments struct {
		Name   string        `json:"name"`
		Level  string        `json:"level"`
		Config *serverConfig `json:"config"`
	}
	str := map[string]any{"type": "string"}
	strMap := map[string]any{"type": "object", "additionalProperties": str}
	var tools []*durable.ToolRegistration
	add := func(action, description string, invoke func(context.Context, string, arguments) (any, error)) {
		props := map[string]any{}
		required := []string{}
		if action != "list" {
			props["name"] = map[string]any{"type": "string", "pattern": serverName.String()}
			props["level"] = map[string]any{"type": "string", "enum": []string{"dex", "agent"}}
			required = append(required, "name", "level")
		}
		replay := durable.ReplaySafe
		if action == "set" {
			props["config"] = map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"type": map[string]any{"type": "string", "description": "Use http or stdio. Empty defaults to stdio."}, "command": str,
				"args": map[string]any{"type": "array", "items": str}, "cwd": str, "url": str,
				"env": strMap, "headers": strMap, "disabled": map[string]any{"type": "boolean"},
			}}
			required = append(required, "config")
		}
		if action == "set" || action == "remove" {
			replay = durable.ReplayUnsafe
		}
		tools = append(tools, &durable.ToolRegistration{Name: "mcp_server_" + action, Description: description, Replay: replay,
			Parameters: map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false},
			Execute: func(ctx context.Context, raw any, _ durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
				var args arguments
				var levelID string
				if action != "list" {
					var err error
					args, err = durable.FromJsonValue[arguments](raw)
					if err != nil || !serverName.MatchString(args.Name) {
						return durable.ToolExecutionResult{}, errors.New("invalid MCP server arguments")
					}
					switch args.Level {
					case "dex":
					case "agent":
						levelID = agentID
					default:
						return durable.ToolExecutionResult{}, errors.New("MCP server level must be dex or agent")
					}
				}
				result, err := invoke(ctx, levelID, args)
				if errors.Is(err, sql.ErrNoRows) {
					return durable.ToolExecutionResult{}, errors.New("no MCP server declaration at this level")
				}
				if err != nil {
					return durable.ToolExecutionResult{}, err
				}
				if action == "set" || action == "remove" {
					a.markExtensionChange(agentID, args.Level, "MCP server "+action)
				}
				data, err := json.Marshal(result)
				if err != nil {
					return durable.ToolExecutionResult{}, err
				}
				return durable.ToolExecutionResult{Output: []ai.ToolResultMessageContent{ai.TextContent{Text: string(data)}}}, nil
			},
		})
	}
	add("list", "List the effective MCP servers and their connection status for this agent.", func(_ context.Context, _ string, _ arguments) (any, error) {
		return loaded.serverInfo(), nil
	})
	add("get", "Read one MCP server declaration at the given level. Plain environment and header values and the URL query are hidden.", func(ctx context.Context, levelID string, args arguments) (any, error) {
		cfg, err := store.get(ctx, levelID, args.Name)
		return redactMCPConfig(cfg), err
	})
	const saved = "Saved. The change applies when the agent finishes its current work and reloads."
	add("set", "Replace one MCP server declaration. Use vault references for secrets. Send [hidden] for each environment or header value to keep. Use disabled: true to turn off a wider declaration.", func(ctx context.Context, levelID string, args arguments) (any, error) {
		if args.Config == nil {
			return nil, errors.New("MCP server config is required")
		}
		return saved, store.set(ctx, levelID, args.Name, *args.Config)
	})
	add("remove", "Remove one MCP server declaration. Removal can uncover a wider declaration with the same name.", func(ctx context.Context, levelID string, args arguments) (any, error) {
		return saved, store.remove(ctx, levelID, args.Name)
	})
	return tools
}
