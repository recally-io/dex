package server

import (
	"context"

	"github.com/recally-io/dex/internal/api"
)

func (h *handler) ListInbox(ctx context.Context, req api.ListInboxRequestObject) (api.ListInboxResponseObject, error) {
	include := req.Params.IncludeDismissed != nil && *req.Params.IncludeDismissed
	items, err := h.Automations.Inbox(ctx, include)
	if err != nil {
		return nil, err
	}
	return api.ListInbox200JSONResponse{Items: items}, nil
}
func (h *handler) ReadInboxItem(ctx context.Context, req api.ReadInboxItemRequestObject) (api.ReadInboxItemResponseObject, error) {
	if err := h.Automations.ReadInboxItem(ctx, req.RunId); err != nil {
		return nil, err
	}
	return api.ReadInboxItem200JSONResponse(ok), nil
}
func (h *handler) DismissInboxItem(ctx context.Context, req api.DismissInboxItemRequestObject) (api.DismissInboxItemResponseObject, error) {
	if err := h.Automations.DismissInboxItem(ctx, req.RunId); err != nil {
		return nil, err
	}
	return api.DismissInboxItem200JSONResponse(ok), nil
}
