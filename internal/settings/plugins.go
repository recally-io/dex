package settings

import (
	"context"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// EnabledPlugins returns the names of the plugins that the owner turned on. dex reads them at start.
func (s *Service) EnabledPlugins(ctx context.Context) ([]string, error) {
	return s.q.ListEnabledPlugins(ctx)
}

// SetPlugin stores the choice for one plugin. It takes effect when dex starts again.
func (s *Service) SetPlugin(ctx context.Context, name string, enabled bool) error {
	var on int64
	if enabled {
		on = 1
	}
	return s.q.SetPluginEnabled(ctx, sqlc.SetPluginEnabledParams{Name: name, Enabled: on})
}
