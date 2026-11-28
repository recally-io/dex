package server

import (
	"context"

	"github.com/recally-io/dex/internal/api"
)

func (h *handler) BeginWechatLogin(ctx context.Context, _ api.BeginWechatLoginRequestObject) (api.BeginWechatLoginResponseObject, error) {
	view, err := h.Wechat.Begin(ctx, botID(ctx))
	if err != nil {
		return nil, err
	}
	return api.BeginWechatLogin200JSONResponse(view), nil
}

func (h *handler) PollWechatLogin(ctx context.Context, req api.PollWechatLoginRequestObject) (api.PollWechatLoginResponseObject, error) {
	code := ""
	if req.Body != nil {
		code = deref(req.Body.VerifyCode)
	}
	view, err := h.Wechat.Poll(ctx, botID(ctx), req.LoginId, code)
	if err != nil {
		return nil, err
	}
	return api.PollWechatLogin200JSONResponse(view), nil
}

func (h *handler) DeleteWechat(ctx context.Context, _ api.DeleteWechatRequestObject) (api.DeleteWechatResponseObject, error) {
	if err := h.Wechat.Delete(ctx, botID(ctx)); err != nil {
		return nil, err
	}
	return api.DeleteWechat200JSONResponse(ok), nil
}
