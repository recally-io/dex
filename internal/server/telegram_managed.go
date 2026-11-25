package server

import (
	"context"

	"github.com/recally-io/dex/internal/api"
)

func (h *handler) SetTelegramManager(ctx context.Context, req api.SetTelegramManagerRequestObject) (api.SetTelegramManagerResponseObject, error) {
	if req.Body == nil || req.Body.Token == nil {
		return nil, errInvalid
	}
	if err := h.Agents.SetTelegramManager(ctx, *req.Body.Token); err != nil {
		return nil, err
	}
	return api.SetTelegramManager200JSONResponse(ok), nil
}

func (h *handler) BeginTelegramManagedSetup(ctx context.Context, _ api.BeginTelegramManagedSetupRequestObject) (api.BeginTelegramManagedSetupResponseObject, error) {
	setup, err := h.Agents.BeginTelegramManagedSetup(ctx, agentID(ctx))
	if err != nil {
		return nil, err
	}
	return api.BeginTelegramManagedSetup200JSONResponse(setup), nil
}

func (h *handler) GetTelegramManagedSetup(ctx context.Context, req api.GetTelegramManagedSetupRequestObject) (api.GetTelegramManagedSetupResponseObject, error) {
	setup, err := h.Agents.GetTelegramManagedSetup(ctx, agentID(ctx), req.SetupId)
	if err != nil {
		return nil, err
	}
	return api.GetTelegramManagedSetup200JSONResponse(setup), nil
}

func (h *handler) CancelTelegramManagedSetup(ctx context.Context, req api.CancelTelegramManagedSetupRequestObject) (api.CancelTelegramManagedSetupResponseObject, error) {
	if err := h.Agents.CancelTelegramManagedSetup(ctx, agentID(ctx), req.SetupId); err != nil {
		return nil, err
	}
	return api.CancelTelegramManagedSetup200JSONResponse(ok), nil
}
