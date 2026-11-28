package server

import (
	"context"
	"encoding/json/v2"

	"github.com/recally-io/dex/internal/api"
)

func (h *handler) integrationView(ctx context.Context) (api.BotIntegrations, error) {
	info, err := h.Engine.Extensions(ctx, botID(ctx))
	if err != nil {
		return api.BotIntegrations{}, err
	}
	var out api.BotIntegrations
	raw, err := json.Marshal(info)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
func (h *handler) GetBotIntegrations(ctx context.Context, _ api.GetBotIntegrationsRequestObject) (api.GetBotIntegrationsResponseObject, error) {
	out, err := h.integrationView(ctx)
	return api.GetBotIntegrations200JSONResponse(out), err
}
func (h *handler) CreateMcpServer(ctx context.Context, req api.CreateMcpServerRequestObject) (api.CreateMcpServerResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	// The body type decodes strictly, so unknown fields never reach this point.
	if err := h.Engine.CreateMCPServer(ctx, botID(ctx), *req.Body); err != nil {
		return nil, err
	}
	return api.CreateMcpServer201JSONResponse(ok), nil
}
func (h *handler) GetSharedIntegrations(ctx context.Context, _ api.GetSharedIntegrationsRequestObject) (api.GetSharedIntegrationsResponseObject, error) {
	out, err := h.Engine.SharedIntegrations(ctx)
	return api.GetSharedIntegrations200JSONResponse(out), err
}
func (h *handler) CreateSharedMcpServer(ctx context.Context, req api.CreateSharedMcpServerRequestObject) (api.CreateSharedMcpServerResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	if err := h.Engine.CreateSharedMCPServer(ctx, *req.Body); err != nil {
		return nil, err
	}
	return api.CreateSharedMcpServer201JSONResponse(ok), nil
}
func (h *handler) ListVault(ctx context.Context, _ api.ListVaultRequestObject) (api.ListVaultResponseObject, error) {
	list, err := h.Engine.VaultList(botID(ctx))
	if err != nil {
		return nil, err
	}
	out := make(api.ListVault200JSONResponse, 0, len(list))
	for _, entry := range list {
		out = append(out, api.VaultEntry{Name: entry.Name, Scope: api.IntegrationScope(entry.Scope), UpdatedAt: entry.UpdatedAt})
	}
	return out, nil
}
func (h *handler) PutVault(ctx context.Context, req api.PutVaultRequestObject) (api.PutVaultResponseObject, error) {
	if req.Body == nil || req.Body.Value == nil {
		return nil, errInvalid
	}
	err := h.Engine.VaultEdit(botID(ctx), string(req.Body.Scope), req.Body.Name, req.Body.Value)
	return api.PutVault200JSONResponse(ok), err
}
func (h *handler) DeleteVault(ctx context.Context, req api.DeleteVaultRequestObject) (api.DeleteVaultResponseObject, error) {
	err := h.Engine.VaultEdit(botID(ctx), string(req.Params.Scope), req.Params.Name, nil)
	return api.DeleteVault200JSONResponse(ok), err
}
func (h *handler) ReloadBotIntegrations(ctx context.Context, _ api.ReloadBotIntegrationsRequestObject) (api.ReloadBotIntegrationsResponseObject, error) {
	if err := h.Sessions.ReloadExtensions(ctx, botID(ctx)); err != nil {
		return nil, err
	}
	out, err := h.integrationView(ctx)
	return api.ReloadBotIntegrations200JSONResponse(out), err
}
