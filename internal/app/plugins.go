package app

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"slices"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/plugin"
	"github.com/recally-io/dex/internal/worktask"
)

var pluginName = regexp.MustCompile(`^[a-z]+$`)

// startPlugins builds every plugin of this build and keeps the enabled ones. A plugin is cheap to
// build: it starts no work until the app mounts its routes and jobs.
func startPlugins(ctx context.Context, q *sqlc.Queries, factories []plugin.Factory, deps plugin.Deps) (running []plugin.Plugin, available []string, err error) {
	enabled, err := q.ListEnabledPlugins(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, factory := range factories {
		p := factory(deps)
		if !pluginName.MatchString(p.Name) || slices.Contains(available, p.Name) {
			return nil, nil, fmt.Errorf("plugin name %q is invalid or used twice", p.Name)
		}
		if p.Hooks != nil && p.HookLimit <= 0 {
			return nil, nil, fmt.Errorf("plugin %s has hooks without a body limit", p.Name)
		}
		available = append(available, p.Name)
		if slices.Contains(enabled, p.Name) {
			running = append(running, p)
		}
	}
	for _, name := range enabled {
		if !slices.Contains(available, name) {
			slog.Warn("an enabled plugin is not in this build", "plugin", name)
		}
	}
	return running, available, nil
}

func pluginTools(plugins []plugin.Plugin) []worktask.LinkTool {
	var tools []worktask.LinkTool
	for _, p := range plugins {
		tools = append(tools, p.Tools...)
	}
	return tools
}
