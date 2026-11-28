package server

import (
	"context"
	"encoding/json/v2"

	"github.com/recally-io/dex/internal/api"
)

func (h *handler) WatchSession(ctx context.Context, req api.WatchSessionRequestObject) (api.WatchSessionResponseObject, error) {
	body, err := h.Live.OpenWeb(ctx, botID(ctx), req.SessionId)
	if err != nil {
		return nil, err
	}
	return api.WatchSession200TexteventStreamResponse{Body: body}, nil
}

func (h *handler) GetSessionItem(ctx context.Context, req api.GetSessionItemRequestObject) (api.GetSessionItemResponseObject, error) {
	item, err := h.Live.Detail(ctx, botID(ctx), req.SessionId, req.Params.Entry, req.ItemId)
	if err != nil {
		return nil, err
	}
	var out api.GetSessionItem200JSONResponse
	raw, err := json.Marshal(item)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}

func (h *handler) GetSessionHistory(ctx context.Context, req api.GetSessionHistoryRequestObject) (api.GetSessionHistoryResponseObject, error) {
	limit := 100
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	before, beforeNotice := req.Params.Before, req.Params.BeforeNotification
	if (before == nil && beforeNotice == nil) || (before != nil && *before < 1) ||
		(beforeNotice != nil && *beforeNotice == "") || limit < 1 || limit > 200 {
		return nil, errInvalid
	}
	bot := botID(ctx)
	out := api.GetSessionHistory200JSONResponse{Items: []api.Item{}}
	if before != nil {
		page, err := h.Live.History(ctx, bot, req.SessionId, *before, limit)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(page)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
	} else if _, err := h.Sessions.Get(ctx, bot, req.SessionId); err != nil {
		return nil, err
	}
	if beforeNotice != nil {
		page, err := h.Sessions.TimelineNotifications(ctx, bot, req.SessionId, *beforeNotice, 0, limit)
		if err != nil {
			return nil, err
		}
		notices := make([]api.TimelineNotification, len(page.Notifications))
		for i, notice := range page.Notifications {
			notices[i] = api.TimelineNotification{Id: notice.ID, Text: notice.Text, At: notice.At}
		}
		out.Notifications = &notices
		if page.NextBeforeNotification != "" {
			out.NextBeforeNotification = &page.NextBeforeNotification
		}
	}
	return out, nil
}
