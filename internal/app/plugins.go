package app

import (
	"fmt"
	"regexp"

	"github.com/recally-io/dex/internal/plugin"
	"github.com/recally-io/dex/internal/worktask"
)

var pluginName = regexp.MustCompile(`^[a-z]+$`)

func startPlugins(factories []plugin.Factory, deps plugin.Deps) ([]plugin.Plugin, error) {
	plugins := make([]plugin.Plugin, 0, len(factories))
	names := make(map[string]bool)
	for _, factory := range factories {
		p := factory(deps)
		if !pluginName.MatchString(p.Name) || names[p.Name] {
			return nil, fmt.Errorf("plugin name %q is invalid or used twice", p.Name)
		}
		if p.Hooks != nil && p.HookLimit <= 0 {
			return nil, fmt.Errorf("plugin %s has hooks without a body limit", p.Name)
		}
		names[p.Name] = true
		plugins = append(plugins, p)
	}
	return plugins, nil
}

func pluginTools(plugins []plugin.Plugin) []worktask.LinkTool {
	var tools []worktask.LinkTool
	for _, p := range plugins {
		tools = append(tools, p.Tools...)
	}
	return tools
}
