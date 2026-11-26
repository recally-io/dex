package server

import (
	"context"
	"encoding/json/v2"

	"github.com/recally-io/dex/internal/api"
	tasksvc "github.com/recally-io/dex/internal/task"
)

func (h *handler) ListHeldEvents(ctx context.Context, _ api.ListHeldEventsRequestObject) (api.ListHeldEventsResponseObject, error) {
	rows, err := h.Tasks.ListHeld(ctx)
	if err != nil {
		return nil, err
	}
	out := api.ListHeldEvents200JSONResponse{Events: make([]api.HeldEvent, 0, len(rows))}
	if len(rows) > 0 {
		out.Total = rows[0].Total
	}
	for _, row := range rows {
		var objects []tasksvc.Object
		if err := json.Unmarshal([]byte(row.Objects), &objects); err != nil {
			return nil, err
		}
		refs := make([]struct {
			Key  string `json:"key"`
			Type string `json:"type"`
		}, len(objects))
		for i, object := range objects {
			refs[i].Key, refs[i].Type = object.Key, object.Type
		}
		out.Events = append(out.Events, api.HeldEvent{
			Id: row.ID, Namespace: row.Namespace, EventType: row.EventType, Objects: refs, Reason: api.HeldEventReason(row.Disposition),
			Content: row.Content, SourceUrl: row.SourceUrl, AuthorLogin: row.AuthorLogin, AuthorAssociation: row.AuthorAssociation,
			OccurredAt: row.OccurredAt, HeldAt: row.HeldAt,
		})
	}
	return out, nil
}

func (h *handler) AttachHeldEvent(ctx context.Context, req api.AttachHeldEventRequestObject) (api.AttachHeldEventResponseObject, error) {
	if err := h.Tasks.AttachHeld(ctx, req.EventId, req.Body.BotId, req.Body.TaskId); err != nil {
		return nil, taskError(err)
	}
	return api.AttachHeldEvent204Response{}, nil
}

func (h *handler) DismissHeldEvent(ctx context.Context, req api.DismissHeldEventRequestObject) (api.DismissHeldEventResponseObject, error) {
	if err := h.Tasks.DismissHeld(ctx, req.EventId); err != nil {
		return nil, err
	}
	return api.DismissHeldEvent204Response{}, nil
}
