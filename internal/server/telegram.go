package server

import (
	"context"

	"github.com/recally-io/dex/internal/api"
)

func (h *handler) SetTelegram(ctx context.Context, req api.SetTelegramRequestObject) (api.SetTelegramResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	var transport string
	if req.Body.Transport != nil {
		transport = string(*req.Body.Transport)
	}
	if err := h.Telegram.Set(ctx, botID(ctx), req.Body.Token, req.Body.AllowedUsers, req.Body.Options, transport, req.Body.Webhook); err != nil {
		return nil, err
	}
	return api.SetTelegram200JSONResponse(ok), nil
}

func (h *handler) DeleteTelegram(ctx context.Context, _ api.DeleteTelegramRequestObject) (api.DeleteTelegramResponseObject, error) {
	if err := h.Telegram.Delete(ctx, botID(ctx)); err != nil {
		return nil, err
	}
	return api.DeleteTelegram200JSONResponse(ok), nil
}

func (h *handler) CreateTelegramPairing(ctx context.Context, _ api.CreateTelegramPairingRequestObject) (api.CreateTelegramPairingResponseObject, error) {
	link, expires, err := h.Telegram.CreatePairing(ctx, botID(ctx))
	if err != nil {
		return nil, err
	}
	return api.CreateTelegramPairing200JSONResponse{Url: link, ExpiresAt: expires}, nil
}
