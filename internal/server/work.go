package server

import (
	"context"
	"errors"
	"fmt"
	"time"

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
		unusable := agent.ModelID == ""
		if !unusable && len(list) > 0 {
			modelErr := h.Settings.CheckModel(ctx, agent.Provider, agent.ModelID)
			switch {
			case modelErr == nil, errors.Is(modelErr, engine.ErrModelsPending):
			case errors.Is(modelErr, settings.ErrInvalid):
				unusable = true
			default:
				return nil, modelErr
			}
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
