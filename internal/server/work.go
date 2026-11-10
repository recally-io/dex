package server

import (
	"context"
	"fmt"
	"time"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/automation"
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
		for _, a := range list {
			runs, err := h.Automations.Runs(ctx, agent.ID, a.ID, -1)
			if err != nil {
				return nil, err
			}
			filtered := make([]automation.Run, 0)
			for _, run := range runs {
				at := run.CreatedAt
				if run.DueAt != 0 {
					at = run.DueAt
				}
				if at >= from && at < to {
					filtered = append(filtered, run)
				}
			}
			scheduled, err := h.Automations.Scheduled(a, from, to)
			if err != nil {
				return nil, err
			}
			out.Automations = append(out.Automations, api.WorkAutomation{AgentId: agent.ID, Automation: a, Runs: filtered, Scheduled: scheduled, Setup: a.Enabled && agent.ModelID == ""})
		}
	}
	return api.GetWork200JSONResponse(out), nil
}
