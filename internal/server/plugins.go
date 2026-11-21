package server

import (
	"context"
	"slices"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/plugin"
	"github.com/recally-io/dex/internal/settings"
)

func (h *handler) ListPlugins(ctx context.Context, _ api.ListPluginsRequestObject) (api.ListPluginsResponseObject, error) {
	enabled, err := h.Settings.EnabledPlugins(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]api.PluginSetting, 0, len(h.AvailablePlugins))
	for _, name := range h.AvailablePlugins {
		running := slices.ContainsFunc(h.Plugins, func(p plugin.Plugin) bool { return p.Name == name })
		setting := api.PluginSetting{Name: name, Enabled: slices.Contains(enabled, name), Running: running}
		// The error is from the last start. It does not apply after the owner turns the plugin off.
		if reason, ok := h.PluginErrors[name]; ok && setting.Enabled {
			setting.Error = &reason
		}
		out = append(out, setting)
	}
	return api.ListPlugins200JSONResponse{Plugins: out}, nil
}

func (h *handler) SetPlugin(ctx context.Context, req api.SetPluginRequestObject) (api.SetPluginResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	if !slices.Contains(h.AvailablePlugins, req.Name) {
		return nil, settings.ErrNotFound
	}
	if err := h.Settings.SetPlugin(ctx, req.Name, req.Body.Enabled); err != nil {
		return nil, err
	}
	return api.SetPlugin204Response{}, nil
}
