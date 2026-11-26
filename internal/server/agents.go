package server

import (
	"context"

	"github.com/recally-io/dex/internal/agent"
	"github.com/recally-io/dex/internal/api"
)

func (h *handler) ListAgents(ctx context.Context, _ api.ListAgentsRequestObject) (api.ListAgentsResponseObject, error) {
	agents, err := h.Agents.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	return api.ListAgents200JSONResponse{Agents: agents}, nil
}

func (h *handler) CreateAgent(ctx context.Context, req api.CreateAgentRequestObject) (api.CreateAgentResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	b := req.Body
	var thinking *string
	if b.ThinkingLevel != nil {
		level := string(*b.ThinkingLevel)
		thinking = &level
	}
	created, err := h.Agents.CreateAgent(ctx, agent.Patch{
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
	return api.CreateAgent201JSONResponse(created), nil
}

func (h *handler) GetAgent(ctx context.Context, req api.GetAgentRequestObject) (api.GetAgentResponseObject, error) {
	agent, err := h.Agents.Agent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	return api.GetAgent200JSONResponse(agent), nil
}

func (h *handler) UpdateAgent(ctx context.Context, req api.UpdateAgentRequestObject) (api.UpdateAgentResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	if err := h.Agents.SaveAgent(ctx, req.AgentId, *req.Body); err != nil {
		return nil, err
	}
	agent, err := h.Agents.Agent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	return api.UpdateAgent200JSONResponse(agent), nil
}

func (h *handler) GetChannels(ctx context.Context, _ api.GetChannelsRequestObject) (api.GetChannelsResponseObject, error) {
	channels, err := h.Agents.Channels(ctx, agentID(ctx))
	if err != nil {
		return nil, err
	}
	return api.GetChannels200JSONResponse(channels), nil
}

func (h *handler) ListArchivedAgents(ctx context.Context, _ api.ListArchivedAgentsRequestObject) (api.ListArchivedAgentsResponseObject, error) {
	agents, err := h.Agents.ListArchivedAgents(ctx)
	if err != nil {
		return nil, err
	}
	return api.ListArchivedAgents200JSONResponse{Agents: agents}, nil
}

func (h *handler) RestoreAgent(ctx context.Context, req api.RestoreAgentRequestObject) (api.RestoreAgentResponseObject, error) {
	if err := h.Agents.RestoreAgent(ctx, req.AgentId); err != nil {
		return nil, err
	}
	agent, err := h.Agents.Agent(ctx, req.AgentId)
	if err != nil {
		return nil, err
	}
	return api.RestoreAgent200JSONResponse(agent), nil
}

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
