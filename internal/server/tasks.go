package server

import (
	"context"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/chat"
)

func taskView(t chat.Task) api.Task {
	out := api.Task{Id: t.ID, ShortId: t.ShortID, ChatId: t.ChatID, Title: t.Title, Status: api.TaskStatus(t.Status),
		CreatedAt: t.CreatedAt, LastEventAt: t.LastEventAt, EndedAt: t.EndedAt, StopRequestedAt: t.StopRequestedAt, Report: t.Report, Stages: []api.TaskStage{}}
	for _, p := range t.Stages {
		out.Stages = append(out.Stages, api.TaskStage{Id: p.ID, Title: p.Title, AgentId: p.AgentID, Status: api.TaskStageStatus(p.Status),
			Provider: p.Provider, ModelId: p.ModelID, ThinkingLevel: api.ThinkingLevel(p.ThinkingLevel), CreatedAt: p.CreatedAt,
			StartedAt: p.StartedAt, EndedAt: p.EndedAt, LastEventAt: p.LastEventAt, CurrentTool: p.CurrentTool, Report: p.Report})
	}
	return out
}

func (h *handler) ListTasks(ctx context.Context, req api.ListTasksRequestObject) (api.ListTasksResponseObject, error) {
	tasks, err := h.Chats.ListTasks(ctx, h.AgentID, req.ChatId)
	if err != nil {
		return nil, err
	}
	out := api.ListTasks200JSONResponse{Tasks: []api.Task{}}
	for _, t := range tasks {
		out.Tasks = append(out.Tasks, taskView(t))
	}
	return out, nil
}

func (h *handler) GetTask(ctx context.Context, req api.GetTaskRequestObject) (api.GetTaskResponseObject, error) {
	t, err := h.Chats.GetTask(ctx, h.AgentID, req.ChatId, req.TaskId)
	if err != nil {
		return nil, err
	}
	return api.GetTask200JSONResponse(taskView(t)), nil
}

func (h *handler) CancelTask(ctx context.Context, req api.CancelTaskRequestObject) (api.CancelTaskResponseObject, error) {
	t, err := h.Chats.CancelTask(ctx, h.AgentID, req.ChatId, req.TaskId)
	if err != nil {
		return nil, err
	}
	return api.CancelTask202JSONResponse(taskView(t)), nil
}

func (h *handler) WatchTaskStage(ctx context.Context, req api.WatchTaskStageRequestObject) (api.WatchTaskStageResponseObject, error) {
	body, err := h.Live.OpenTask(ctx, req.ChatId, req.TaskId, req.StageId)
	if err != nil {
		return nil, err
	}
	return api.WatchTaskStage200TexteventStreamResponse{Body: body}, nil
}
