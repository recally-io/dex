package server

import (
	"context"
	"slices"

	"github.com/vaayne/dex/internal/api"
	"github.com/vaayne/dex/internal/automation"
)

func scheduleIn(s api.Schedule) automation.Schedule {
	out := automation.Schedule{Kind: string(s.Kind)}
	if s.Minutes != nil {
		out.Minutes = *s.Minutes
	}
	if s.Time != nil {
		out.Time = *s.Time
	}
	if s.Days != nil {
		out.Days = *s.Days
	}
	if s.At != nil {
		out.At = *s.At
	}
	return out
}

func scheduleView(s automation.Schedule) api.Schedule {
	out := api.Schedule{Kind: api.ScheduleKind(s.Kind)}
	if s.Minutes != 0 {
		out.Minutes = &s.Minutes
	}
	if s.Time != "" {
		out.Time = &s.Time
	}
	if s.Days != nil {
		out.Days = new(slices.Clone(s.Days))
	}
	if s.At != 0 {
		out.At = &s.At
	}
	return out
}

func automationIn(in *api.AutomationInput) (automation.Input, error) {
	if in == nil {
		return automation.Input{}, errInvalid
	}
	return automation.Input{
		Name: in.Name, Prompt: in.Prompt, Schedule: scheduleIn(in.Schedule), TimeZone: in.TimeZone,
		Session: string(in.Session), Enabled: in.Enabled,
	}, nil
}

func automationView(a automation.Automation) api.Automation {
	out := api.Automation{
		Id: a.ID, Name: a.Name, Prompt: a.Prompt, Schedule: scheduleView(a.Schedule), TimeZone: a.TimeZone,
		Session: api.AutomationSession(a.Session), Enabled: a.Enabled, NextRuns: a.NextRuns,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
	if out.NextRuns == nil {
		out.NextRuns = []int64{}
	}
	if a.LastRun != nil {
		out.LastRun = new(runView(*a.LastRun))
	}
	return out
}

func runView(r automation.Run) api.AutomationRun {
	out := api.AutomationRun{
		Id: r.ID, CreatedAt: r.CreatedAt, Status: api.AutomationRunStatus(r.Status), Reason: optional(r.Reason),
	}
	if r.DueAt != 0 {
		out.DueAt = &r.DueAt
	}
	if r.ChatID != 0 {
		out.ChatId = &r.ChatID
	}
	return out
}

func (h *handler) ListAutomations(ctx context.Context, _ api.ListAutomationsRequestObject) (api.ListAutomationsResponseObject, error) {
	list, err := h.Automations.List(ctx)
	if err != nil {
		return nil, err
	}
	out := api.ListAutomations200JSONResponse{Automations: make([]api.Automation, len(list))}
	for i, a := range list {
		out.Automations[i] = automationView(a)
	}
	return out, nil
}

func (h *handler) CreateAutomation(ctx context.Context, req api.CreateAutomationRequestObject) (api.CreateAutomationResponseObject, error) {
	in, err := automationIn(req.Body)
	if err != nil {
		return nil, err
	}
	a, err := h.Automations.Create(ctx, in)
	if err != nil {
		return nil, err
	}
	return api.CreateAutomation201JSONResponse(automationView(a)), nil
}

func (h *handler) PreviewSchedule(_ context.Context, req api.PreviewScheduleRequestObject) (api.PreviewScheduleResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	times, err := h.Automations.Preview(scheduleIn(req.Body.Schedule), req.Body.TimeZone)
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
	return api.GetAutomation200JSONResponse(automationView(a)), nil
}

func (h *handler) UpdateAutomation(ctx context.Context, req api.UpdateAutomationRequestObject) (api.UpdateAutomationResponseObject, error) {
	in, err := automationIn(req.Body)
	if err != nil {
		return nil, err
	}
	a, err := h.Automations.Update(ctx, req.AutomationId, in)
	if err != nil {
		return nil, err
	}
	return api.UpdateAutomation200JSONResponse(automationView(a)), nil
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
	return api.RunAutomation202JSONResponse(runView(run)), nil
}

func (h *handler) ListAutomationRuns(ctx context.Context, req api.ListAutomationRunsRequestObject) (api.ListAutomationRunsResponseObject, error) {
	if _, err := h.Automations.Get(ctx, req.AutomationId); err != nil {
		return nil, err
	}
	runs, err := h.Automations.Runs(ctx, req.AutomationId, 50)
	if err != nil {
		return nil, err
	}
	out := api.ListAutomationRuns200JSONResponse{Runs: make([]api.AutomationRun, len(runs))}
	for i, r := range runs {
		out.Runs[i] = runView(r)
	}
	return out, nil
}
