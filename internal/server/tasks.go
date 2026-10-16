package server

import (
	"context"
	"database/sql"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/db/sqlc"
)

func taskView(t sqlc.Task) api.Task {
	time := func(n sql.NullInt64) *int64 {
		if n.Valid {
			return new(n.Int64)
		}
		return nil
	}
	return api.Task{
		Id: t.ID, Title: t.Title, Status: api.TaskStatus(t.Status), Report: t.Report,
		CreatedAt: t.CreatedAt, StartedAt: time(t.StartedAt), EndedAt: time(t.EndedAt), ConversationId: t.ConversationID,
	}
}

func (h *handler) ListTasks(ctx context.Context, _ api.ListTasksRequestObject) (api.ListTasksResponseObject, error) {
	rows, err := h.Chats.TaskRecords(ctx, h.AgentID)
	if err != nil {
		return nil, err
	}
	out := api.ListTasks200JSONResponse{Tasks: make([]api.Task, len(rows))}
	for i, t := range rows {
		out.Tasks[i] = taskView(t)
	}
	return out, nil
}

func (h *handler) GetTask(ctx context.Context, req api.GetTaskRequestObject) (api.GetTaskResponseObject, error) {
	t, err := h.Chats.TaskRecord(ctx, h.AgentID, req.TaskId)
	if err != nil {
		return nil, err
	}
	return api.GetTask200JSONResponse(taskView(t)), nil
}
