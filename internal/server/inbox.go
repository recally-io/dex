package server

import (
	"context"
	"errors"

	"github.com/recally-io/dex/internal/agent"
	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/settings"
)

// GetWork returns sessions, Automation activity, and Tasks awaiting Owner review.
func (h *handler) GetInboxSummary(ctx context.Context, _ api.GetInboxSummaryRequestObject) (api.GetInboxSummaryResponseObject, error) {
	agents, err := h.Agents.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	inbox, err := h.Automations.Inbox(ctx, true)
	if err != nil {
		return nil, err
	}
	reviews, err := h.Tasks.ListOwnerReviews(ctx)
	if err != nil {
		return nil, err
	}
	out := api.InboxSummary{Inbox: inbox, Sessions: []api.InboxSummarySession{}, Automations: []api.InboxSummaryAutomation{}, TaskReviews: taskViewRefs(reviews)}
	for _, agent := range agents {
		main, err := h.Chats.ExistingMain(ctx, agent.ID)
		if err != nil {
			return nil, err
		}
		if main != nil {
			out.Sessions = append(out.Sessions, api.InboxSummarySession{AgentId: agent.ID, Chat: chatView(*main)})
		}
		response, err := h.listChats(ctx, agent.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range response.Chats {
			if c.Channel != api.ChatChannelAutomation {
				out.Sessions = append(out.Sessions, api.InboxSummarySession{AgentId: agent.ID, Chat: c})
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
			out.Automations = append(out.Automations, api.InboxSummaryAutomation{AgentId: agent.ID, Automation: a, Setup: a.Enabled && unusable})
		}
	}
	return api.GetInboxSummary200JSONResponse(out), nil
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
