package server

import (
	"context"
	"strings"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/settings"
)

// convert converts the strings of pi-ai enums to their API types.
func convert[T ~string, S ~string](values []S) []T {
	out := make([]T, len(values))
	for i, v := range values {
		out[i] = T(v)
	}
	return out
}

func (h *handler) ListModels(ctx context.Context, _ api.ListModelsRequestObject) (api.ListModelsResponseObject, error) {
	models, err := h.Settings.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := api.ListModels200JSONResponse{Models: make([]api.Model, len(models))}
	for i, m := range models {
		out.Models[i] = api.Model{
			Provider: m.Provider, Id: m.ID, Name: m.Name, Reasoning: m.Reasoning,
			Input: convert[api.ModelInput](m.Input), ThinkingLevels: convert[api.ThinkingLevel](m.ThinkingLevels),
		}
	}
	return out, nil
}

func (h *handler) ListProviderModels(ctx context.Context, req api.ListProviderModelsRequestObject) (api.ListProviderModelsResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	b := req.Body
	models, err := h.Settings.ListProviderModels(ctx, settings.Provider{
		ID: deref(b.ProviderId), API: b.Api, BaseURL: strings.TrimSpace(b.BaseUrl), APIKey: strings.TrimSpace(deref(b.ApiKey)),
	})
	if err != nil {
		return nil, err
	}
	out := api.ListProviderModels200JSONResponse{Models: make([]api.ProviderModel, len(models))}
	for i, m := range models {
		out.Models[i] = api.ProviderModel{Id: m.ID, Name: m.Name}
	}
	return out, nil
}
