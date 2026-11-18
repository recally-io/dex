package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/recally-io/dex/internal/agent"
	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/settings"
)

// GetWork returns a read-only view of sessions and automation activity across bots.
func (h *handler) GetWork(ctx context.Context, req api.GetWorkRequestObject) (api.GetWorkResponseObject, error) {
	from, to := req.Params.From, req.Params.To
	if from < 0 || to <= from || to-from > int64(31*24*time.Hour/time.Millisecond) {
		return nil, fmt.Errorf("%w: work range must be at most 31 days", errInvalid)
	}
	agents, err := h.Agents.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	inbox, err := h.Automations.Inbox(ctx, true)
	if err != nil {
		return nil, err
	}
	out := api.Work{Agents: agents, Inbox: inbox, Sessions: []api.WorkSession{}, Automations: []api.WorkAutomation{}}
	for _, agent := range agents {
		main, err := h.Chats.ExistingMain(ctx, agent.ID)
		if err != nil {
			return nil, err
		}
		if main != nil {
			out.Sessions = append(out.Sessions, api.WorkSession{AgentId: agent.ID, Chat: chatView(*main)})
		}
		response, err := h.listChats(ctx, agent.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range response.Chats {
			if c.Channel != api.ChatChannelAutomation {
				out.Sessions = append(out.Sessions, api.WorkSession{AgentId: agent.ID, Chat: c})
			}
		}
		list, err := h.Automations.List(ctx, agent.ID)
		if err != nil {
			return nil, err
		}
		var unusable bool
		if len(list) > 0 {
			usable, err := h.agentModelUsable(ctx, agent)
			if err != nil {
				return nil, err
			}
			unusable = !usable
		}
		for _, a := range list {
			runs, err := h.Automations.RunsInRange(ctx, agent.ID, a.ID, from, to, -1)
			if err != nil {
				return nil, err
			}
			scheduled, err := h.Automations.Scheduled(a, from, to)
			if err != nil {
				return nil, err
			}
			out.Automations = append(out.Automations, api.WorkAutomation{AgentId: agent.ID, Automation: a, Runs: runs, Scheduled: scheduled, Setup: a.Enabled && unusable})
		}
	}
	return api.GetWork200JSONResponse(out), nil
}

func (h *handler) agentModelUsable(ctx context.Context, a agent.Agent) (bool, error) {
	candidates := [2][2]string{{a.Provider, a.ModelID}, {a.FallbackProvider, a.FallbackModelID}}
	for _, m := range candidates {
		if m[1] == "" {
			continue
		}
		err := h.Settings.CheckModel(ctx, m[0], m[1])
		switch {
		case err == nil, errors.Is(err, engine.ErrModelsPending):
			return true, nil
		case errors.Is(err, settings.ErrInvalid):
		default:
			return false, err
		}
	}
	return false, nil
}
