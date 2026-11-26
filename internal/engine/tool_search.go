package engine

import (
	"context"
	"strings"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/durable"
	"github.com/MichaelKinsy/PiG/durable/toolsearch"
)

func toolSearchExtension() *durable.Extension {
	extension := toolsearch.CreateExtension()
	tool := extension.Tools[0]
	tool.Description = "Search tools available to this conversation, including built-in tools and MCP tools. Matching deferred tools become available on the next model call."
	tool.Parameters = ObjectSchema([]string{"query"}, map[string]any{
		"query": map[string]any{"type": "string", "description": "Search query for tools."},
		"limit": map[string]any{"type": "number", "description": "Maximum number of tools to return. Defaults to 8."},
	})
	search := tool.Execute
	tool.Execute = func(ctx context.Context, args any, api durable.ToolExecutionApi) (durable.ToolExecutionResult, error) {
		bot, err := api.Agent(ctx)
		if err != nil {
			return durable.ToolExecutionResult{}, err
		}
		offered := make(map[string]bool, len(bot.Tools))
		candidates := make([]*durable.ToolRegistration, 0, len(bot.Deferred)+len(bot.Tools))
		catalog := make(map[string]*durable.ToolRegistration, cap(candidates))
		for _, entry := range bot.Tools {
			offered[entry.Name] = true
			catalog[entry.Name] = entry
			candidates = append(candidates, entry)
		}
		for _, entry := range bot.Deferred {
			if catalog[entry.Name] == nil {
				catalog[entry.Name] = entry
				candidates = append(candidates, entry)
			}
		}
		// Use PiG's ranking without changing which tools the model receives up front.
		bot.Deferred = candidates
		result, err := search(ctx, args, toolSearchCatalog{ToolExecutionApi: api, bot: bot})
		if err != nil || result.Control == nil {
			return result, err
		}
		matches := result.Control.AddTools
		loaded := []string{}
		var output strings.Builder
		output.WriteString("Matching tools:\n")
		for _, name := range matches {
			if !offered[name] {
				loaded = append(loaded, name)
			}
			description, _, _ := strings.Cut(strings.TrimSpace(catalog[name].Description), "\n")
			output.WriteString("- " + name + ": " + description + "\n")
		}
		if len(loaded) > 0 {
			output.WriteString("Deferred matches are available from your next call.")
			result.Control.AddTools = loaded
		} else {
			result.Control = nil
		}
		result.Output = []ai.ToolResultMessageContent{ai.TextContent{Text: output.String()}}
		result.Details = map[string]any{"loaded": loaded, "matches": matches}
		return result, nil
	}
	return extension
}

type toolSearchCatalog struct {
	durable.ToolExecutionApi
	bot durable.Agent
}

func (c toolSearchCatalog) Agent(context.Context) (durable.Agent, error) {
	return c.bot, nil
}
