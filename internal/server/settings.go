package server

import (
	"context"
	"strings"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/settings"
)

func (h *handler) GetSettings(ctx context.Context, _ api.GetSettingsRequestObject) (api.GetSettingsResponseObject, error) {
	s, err := h.Settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	out := api.GetSettings200JSONResponse{
		Providers: make([]api.Provider, len(s.Providers)),
		Builtins:  make([]api.ProviderInfo, len(s.Builtins)),
		Apis:      s.APIs,
	}
	for i, p := range s.Providers {
		out.Providers[i] = providerView(p)
	}
	for i, b := range s.Builtins {
		out.Builtins[i] = api.ProviderInfo{Id: b.ID, Name: b.Name, Source: optional(b.Source)}
	}
	return out, nil
}

func (h *handler) GetOnePassword(ctx context.Context, _ api.GetOnePasswordRequestObject) (api.GetOnePasswordResponseObject, error) {
	o, err := h.Settings.OnePassword(ctx)
	return api.GetOnePassword200JSONResponse(onePasswordView(o)), err
}

func (h *handler) SetOnePassword(ctx context.Context, req api.SetOnePasswordRequestObject) (api.SetOnePasswordResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	b := req.Body
	o, err := h.Settings.SetOnePassword(ctx, settings.OnePasswordInput{
		Mode: string(b.Mode), ConnectHost: deref(b.ConnectHost), ConnectToken: deref(b.ConnectToken), ServiceAccountToken: deref(b.ServiceAccountToken),
	})
	if err != nil {
		return nil, err
	}
	return api.SetOnePassword200JSONResponse(onePasswordView(o)), nil
}

func onePasswordView(o settings.OnePassword) api.OnePassword {
	return api.OnePassword{
		Mode: api.OnePasswordMode(o.Mode), ConnectHost: o.ConnectHost, ConnectTokenSet: o.ConnectTokenSet,
		ServiceAccountTokenSet: o.ServiceAccountTokenSet, Cli: o.CLI,
	}
}

func providerView(p settings.Provider) api.Provider {
	return api.Provider{
		Id: p.ID, Name: p.Name, Api: p.API, BaseUrl: p.BaseURL, KeySet: p.KeySet, Models: p.Models,
		ModelCount: &p.ModelCount, Error: optional(p.Error),
	}
}

func (h *handler) SetProvider(ctx context.Context, req api.SetProviderRequestObject) (api.SetProviderResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	b := req.Body
	p, err := h.Settings.SetProvider(ctx, settings.Provider{
		ID: req.ProviderId, API: b.Api, Name: deref(b.Name), BaseURL: strings.TrimSpace(deref(b.BaseUrl)),
		APIKey: strings.TrimSpace(deref(b.ApiKey)), Models: derefSlice(b.Models),
	})
	if err != nil {
		return nil, err
	}
	return api.SetProvider200JSONResponse(providerView(p)), nil
}

func derefSlice(s *[]string) []string {
	if s == nil {
		return nil
	}
	return *s
}

func (h *handler) DeleteProvider(ctx context.Context, req api.DeleteProviderRequestObject) (api.DeleteProviderResponseObject, error) {
	if err := h.Settings.DeleteProvider(ctx, req.ProviderId); err != nil {
		return nil, err
	}
	return api.DeleteProvider200JSONResponse(ok), nil
}
