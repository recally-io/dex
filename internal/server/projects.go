package server

import (
	"context"
	"strings"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/bot"
)

func (h *handler) ListDirectories(ctx context.Context, req api.ListDirectoriesRequestObject) (api.ListDirectoriesResponseObject, error) {
	id := "default"
	if req.Params.BotId != nil && strings.TrimSpace(*req.Params.BotId) != "" {
		id = strings.TrimSpace(*req.Params.BotId)
		row, err := h.Bots.Bot(ctx, id)
		if err == nil && row.ArchivedAt != 0 {
			return nil, bot.ErrNotFound
		}
		if err != nil {
			return nil, err
		}
	}
	listing, err := h.Sessions.ListDirectories(ctx, id, deref(req.Params.RunnerId), req.Params.Path)
	if err != nil {
		return nil, err
	}
	return api.ListDirectories200JSONResponse(listing), nil
}

func (h *handler) ListRecentProjects(ctx context.Context, req api.ListRecentProjectsRequestObject) (api.ListRecentProjectsResponseObject, error) {
	projects, err := h.Sessions.RecentProjects(ctx, botID(ctx), deref(req.Params.RunnerId))
	if err != nil {
		return nil, err
	}
	return api.ListRecentProjects200JSONResponse{Projects: projects}, nil
}
