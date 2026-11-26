package server

import (
	"context"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/session"
)

func (h *handler) ListAutomations(ctx context.Context, _ api.ListAutomationsRequestObject) (api.ListAutomationsResponseObject, error) {
	list, err := h.Automations.List(ctx, agentID(ctx))
	if err != nil {
		return nil, err
	}
	return api.ListAutomations200JSONResponse{Automations: list}, nil
}

func (h *handler) ListAutomationTargets(ctx context.Context, _ api.ListAutomationTargetsRequestObject) (api.ListAutomationTargetsResponseObject, error) {
	rows, err := h.Automations.ListTargetCandidates(ctx, agentID(ctx))
	if err != nil {
		return nil, err
	}
	targets := make([]api.AutomationTarget, 0, len(rows))
	for _, row := range rows {
		if session.AutomationTargetEligible(row, agentID(ctx)) {
			targets = append(targets, api.AutomationTarget{Id: row.ID, Title: row.Title, Main: row.ChannelKey == session.MainKey})
		}
	}
	return api.ListAutomationTargets200JSONResponse{Targets: targets}, nil
}

func (h *handler) CreateAutomation(ctx context.Context, req api.CreateAutomationRequestObject) (api.CreateAutomationResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	a, err := h.Automations.Create(ctx, agentID(ctx), *req.Body)
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
	a, err := h.Automations.Get(ctx, agentID(ctx), req.AutomationId)
	if err != nil {
		return nil, err
	}
	return api.GetAutomation200JSONResponse(a), nil
}

func (h *handler) UpdateAutomation(ctx context.Context, req api.UpdateAutomationRequestObject) (api.UpdateAutomationResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	a, err := h.Automations.Update(ctx, agentID(ctx), req.AutomationId, *req.Body)
	if err != nil {
		return nil, err
	}
	return api.UpdateAutomation200JSONResponse(a), nil
}

func (h *handler) DeleteAutomation(ctx context.Context, req api.DeleteAutomationRequestObject) (api.DeleteAutomationResponseObject, error) {
	if err := h.Automations.Delete(ctx, agentID(ctx), req.AutomationId); err != nil {
		return nil, err
	}
	return api.DeleteAutomation200JSONResponse(ok), nil
}

func (h *handler) RunAutomation(ctx context.Context, req api.RunAutomationRequestObject) (api.RunAutomationResponseObject, error) {
	run, err := h.Automations.RunNow(ctx, agentID(ctx), req.AutomationId)
	if err != nil {
		return nil, err
	}
	return api.RunAutomation202JSONResponse(run), nil
}

func (h *handler) ListAutomationRuns(ctx context.Context, req api.ListAutomationRunsRequestObject) (api.ListAutomationRunsResponseObject, error) {
	runs, err := h.Automations.Runs(ctx, agentID(ctx), req.AutomationId, -1)
	if err != nil {
		return nil, err
	}
	return api.ListAutomationRuns200JSONResponse{Runs: runs}, nil
}

func (h *handler) ListAutomationResults(ctx context.Context, req api.ListAutomationResultsRequestObject) (api.ListAutomationResultsResponseObject, error) {
	include := req.Params.IncludeDismissed != nil && *req.Params.IncludeDismissed
	results, err := h.Automations.Results(ctx, include)
	if err != nil {
		return nil, err
	}
	return api.ListAutomationResults200JSONResponse{Results: results}, nil
}

func (h *handler) ReadAutomationResult(ctx context.Context, req api.ReadAutomationResultRequestObject) (api.ReadAutomationResultResponseObject, error) {
	if err := h.Automations.ReadResult(ctx, req.RunId); err != nil {
		return nil, err
	}
	return api.ReadAutomationResult200JSONResponse(ok), nil
}

func (h *handler) DismissAutomationResult(ctx context.Context, req api.DismissAutomationResultRequestObject) (api.DismissAutomationResultResponseObject, error) {
	if err := h.Automations.DismissResult(ctx, req.RunId); err != nil {
		return nil, err
	}
	return api.DismissAutomationResult200JSONResponse(ok), nil
}
