package server

import (
	"context"
	"slices"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/plugin"
	"github.com/recally-io/dex/internal/settings"
)

func (h *handler) ListSources(ctx context.Context, _ api.ListSourcesRequestObject) (api.ListSourcesResponseObject, error) {
	enabled, err := h.Settings.EnabledPlugins(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]api.SourceSetting, 0, len(h.AvailablePlugins))
	for _, name := range h.AvailablePlugins {
		running := slices.ContainsFunc(h.Plugins, func(p plugin.Plugin) bool { return p.Name == name })
		setting := api.SourceSetting{Name: name, Enabled: slices.Contains(enabled, name), Running: running}
		// The error is from the last start. It does not apply after the owner turns the plugin off.
		if reason, ok := h.PluginErrors[name]; ok && setting.Enabled {
			setting.Error = &reason
		}
		out = append(out, setting)
	}
	return api.ListSources200JSONResponse{Sources: out}, nil
}

func (h *handler) SetSourceEnabled(ctx context.Context, req api.SetSourceEnabledRequestObject) (api.SetSourceEnabledResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	if !slices.Contains(h.AvailablePlugins, req.Name) {
		return nil, settings.ErrNotFound
	}
	if err := h.Settings.SetPlugin(ctx, req.Name, req.Body.Enabled); err != nil {
		return nil, err
	}
	return api.SetSourceEnabled204Response{}, nil
}
