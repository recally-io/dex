package server

import (
	"context"

	"github.com/vaayne/dex/internal/api"
)

func (h *handler) ListAutomations(ctx context.Context, _ api.ListAutomationsRequestObject) (api.ListAutomationsResponseObject, error) {
	list, err := h.Automations.List(ctx)
	if err != nil {
		return nil, err
	}
	return api.ListAutomations200JSONResponse{Automations: list}, nil
}

func (h *handler) CreateAutomation(ctx context.Context, req api.CreateAutomationRequestObject) (api.CreateAutomationResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	a, err := h.Automations.Create(ctx, *req.Body)
	if err != nil {
		return nil, err
	}
	return api.CreateAutomation201JSONResponse(a), nil
}

func (h *handler) PreviewSchedule(_ context.Context, req api.PreviewScheduleRequestObject) (api.PreviewScheduleResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	times, err := h.Automations.Preview(req.Body.Schedule, req.Body.TimeZone)
	if err != nil {
		return nil, err
	}
	return api.PreviewSchedule200JSONResponse{NextRuns: times}, nil
}

func (h *handler) GetAutomation(ctx context.Context, req api.GetAutomationRequestObject) (api.GetAutomationResponseObject, error) {
	a, err := h.Automations.Get(ctx, req.AutomationId)
	if err != nil {
		return nil, err
	}
	return api.GetAutomation200JSONResponse(a), nil
}

func (h *handler) UpdateAutomation(ctx context.Context, req api.UpdateAutomationRequestObject) (api.UpdateAutomationResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	a, err := h.Automations.Update(ctx, req.AutomationId, *req.Body)
	if err != nil {
		return nil, err
	}
	return api.UpdateAutomation200JSONResponse(a), nil
}

func (h *handler) DeleteAutomation(ctx context.Context, req api.DeleteAutomationRequestObject) (api.DeleteAutomationResponseObject, error) {
	if err := h.Automations.Delete(ctx, req.AutomationId); err != nil {
		return nil, err
	}
	return api.DeleteAutomation200JSONResponse(ok), nil
}

func (h *handler) RunAutomation(ctx context.Context, req api.RunAutomationRequestObject) (api.RunAutomationResponseObject, error) {
	run, err := h.Automations.RunNow(ctx, req.AutomationId)
	if err != nil {
		return nil, err
	}
	return api.RunAutomation202JSONResponse(run), nil
}

func (h *handler) ListAutomationRuns(ctx context.Context, req api.ListAutomationRunsRequestObject) (api.ListAutomationRunsResponseObject, error) {
	if _, err := h.Automations.Get(ctx, req.AutomationId); err != nil {
		return nil, err
	}
	runs, err := h.Automations.Runs(ctx, req.AutomationId, 50)
	if err != nil {
		return nil, err
	}
	return api.ListAutomationRuns200JSONResponse{Runs: runs}, nil
}
