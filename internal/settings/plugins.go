package settings

import (
	"context"
)

// EnabledPlugins returns the names of the plugins that the owner turned on. dex reads them at start.
func (s *Service) EnabledPlugins(ctx context.Context) ([]string, error) {
	return s.q.ListEnabledPlugins(ctx)
}

// SetPlugin stores the choice for one plugin. It takes effect when dex starts again. Turning a
// plugin off keeps its tables and rows.
func (s *Service) SetPlugin(ctx context.Context, name string, enabled bool) error {
	if enabled {
		return s.q.EnablePlugin(ctx, name)
	}
	return s.q.DisablePlugin(ctx, name)
}
