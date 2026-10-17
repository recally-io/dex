package server

import (
	"context"

	"github.com/recally-io/dex/internal/api"
)

func (h *handler) GetFeishu(ctx context.Context, _ api.GetFeishuRequestObject) (api.GetFeishuResponseObject, error) {
	view, err := h.Settings.GetFeishu(ctx, agentID(ctx))
	if err != nil {
		return nil, err
	}
	return api.GetFeishu200JSONResponse(view), nil
}
func (h *handler) SetFeishu(ctx context.Context, req api.SetFeishuRequestObject) (api.SetFeishuResponseObject, error) {
	view, err := h.Settings.SetFeishu(ctx, agentID(ctx), req.Body.AppId, req.Body.AppSecret, req.Body.Brand)
	if err != nil {
		return nil, err
	}
	return api.SetFeishu200JSONResponse(view), nil
}
func (h *handler) DeleteFeishu(ctx context.Context, _ api.DeleteFeishuRequestObject) (api.DeleteFeishuResponseObject, error) {
	if err := h.Settings.DeleteFeishu(ctx, agentID(ctx)); err != nil {
		return nil, err
	}
	return api.DeleteFeishu200JSONResponse(ok), nil
}
func (h *handler) BeginFeishu(ctx context.Context, req api.BeginFeishuRequestObject) (api.BeginFeishuResponseObject, error) {
	view, err := h.Settings.BeginFeishu(ctx, agentID(ctx), req.Body.Brand)
	if err != nil {
		return nil, err
	}
	return api.BeginFeishu200JSONResponse(view), nil
}
func (h *handler) PollFeishu(ctx context.Context, req api.PollFeishuRequestObject) (api.PollFeishuResponseObject, error) {
	view, err := h.Settings.PollFeishu(ctx, agentID(ctx), req.Body.Session)
	if err != nil {
		return nil, err
	}
	return api.PollFeishu200JSONResponse(view), nil
}
