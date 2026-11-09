package server

import (
	"context"

	"github.com/recally-io/dex/internal/api"
)

func (h *handler) GetNotificationTargets(ctx context.Context, _ api.GetNotificationTargetsRequestObject) (api.GetNotificationTargetsResponseObject, error) {
	targets, err := h.Agents.NotificationTargets(ctx, agentID(ctx))
	if err != nil {
		return nil, err
	}
	return api.GetNotificationTargets200JSONResponse(targets), nil
}

func (h *handler) SetPrimaryNotificationTarget(ctx context.Context, req api.SetPrimaryNotificationTargetRequestObject) (api.SetPrimaryNotificationTargetResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	if err := h.Agents.SetPrimaryNotificationTarget(ctx, agentID(ctx), req.Body.TargetId); err != nil {
		return nil, err
	}
	return api.SetPrimaryNotificationTarget200JSONResponse(ok), nil
}
