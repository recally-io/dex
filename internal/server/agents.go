package server

import (
	"context"

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
	agent, err := h.Agents.CreateAgent(ctx, *req.Body)
	if err != nil {
		return nil, err
	}
	return api.CreateAgent201JSONResponse(agent), nil
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
