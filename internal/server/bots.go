package server

import (
	"context"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/bot"
)

func (h *handler) ListBots(ctx context.Context, _ api.ListBotsRequestObject) (api.ListBotsResponseObject, error) {
	bots, err := h.Bots.ListBots(ctx)
	if err != nil {
		return nil, err
	}
	return api.ListBots200JSONResponse{Bots: bots}, nil
}

func (h *handler) CreateBot(ctx context.Context, req api.CreateBotRequestObject) (api.CreateBotResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	b := req.Body
	var thinking *string
	if b.ThinkingLevel != nil {
		level := string(*b.ThinkingLevel)
		thinking = &level
	}
	created, err := h.Bots.CreateBot(ctx, bot.Patch{
		Name:             &b.Name,
		Provider:         b.Provider,
		ModelID:          b.ModelId,
		ThinkingLevel:    thinking,
		FallbackProvider: b.FallbackProvider,
		FallbackModelID:  b.FallbackModelId,
		Instructions:     b.Instructions,
		RunnerID:         b.RunnerId,
	})
	if err != nil {
		return nil, err
	}
	return api.CreateBot201JSONResponse(created), nil
}

func (h *handler) GetBot(ctx context.Context, req api.GetBotRequestObject) (api.GetBotResponseObject, error) {
	bot, err := h.Bots.Bot(ctx, req.BotId)
	if err != nil {
		return nil, err
	}
	return api.GetBot200JSONResponse(bot), nil
}

func (h *handler) UpdateBot(ctx context.Context, req api.UpdateBotRequestObject) (api.UpdateBotResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	if err := h.Bots.SaveBot(ctx, req.BotId, *req.Body); err != nil {
		return nil, err
	}
	bot, err := h.Bots.Bot(ctx, req.BotId)
	if err != nil {
		return nil, err
	}
	return api.UpdateBot200JSONResponse(bot), nil
}

func (h *handler) GetChannels(ctx context.Context, _ api.GetChannelsRequestObject) (api.GetChannelsResponseObject, error) {
	channels, err := h.Pairing.Channels(ctx, botID(ctx))
	if err != nil {
		return nil, err
	}
	return api.GetChannels200JSONResponse(channels), nil
}

func (h *handler) ListArchivedBots(ctx context.Context, _ api.ListArchivedBotsRequestObject) (api.ListArchivedBotsResponseObject, error) {
	bots, err := h.Bots.ListArchivedBots(ctx)
	if err != nil {
		return nil, err
	}
	return api.ListArchivedBots200JSONResponse{Bots: bots}, nil
}

func (h *handler) RestoreBot(ctx context.Context, req api.RestoreBotRequestObject) (api.RestoreBotResponseObject, error) {
	if err := h.Config.RestoreBot(ctx, req.BotId); err != nil {
		return nil, err
	}
	bot, err := h.Bots.Bot(ctx, req.BotId)
	if err != nil {
		return nil, err
	}
	return api.RestoreBot200JSONResponse(bot), nil
}

func (h *handler) GetNotificationTargets(ctx context.Context, _ api.GetNotificationTargetsRequestObject) (api.GetNotificationTargetsResponseObject, error) {
	targets, err := h.Pairing.NotificationTargets(ctx, botID(ctx))
	if err != nil {
		return nil, err
	}
	return api.GetNotificationTargets200JSONResponse(targets), nil
}

func (h *handler) SetPrimaryNotificationTarget(ctx context.Context, req api.SetPrimaryNotificationTargetRequestObject) (api.SetPrimaryNotificationTargetResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	if err := h.Pairing.SetPrimaryNotificationTarget(ctx, botID(ctx), req.Body.TargetId); err != nil {
		return nil, err
	}
	return api.SetPrimaryNotificationTarget200JSONResponse(ok), nil
}
