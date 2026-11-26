package server

import (
	"context"
	"errors"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/automation"
	"github.com/recally-io/dex/internal/bot"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
	"github.com/recally-io/dex/internal/settings"
	"github.com/recally-io/dex/internal/task"
)

func (h *handler) GetBoardAttention(ctx context.Context, _ api.GetBoardAttentionRequestObject) (api.GetBoardAttentionResponseObject, error) {
	bots, err := h.Bots.ListBots(ctx)
	if err != nil {
		return nil, err
	}
	results, err := h.Automations.Results(ctx, false)
	if err != nil {
		return nil, err
	}
	reviews, err := h.Tasks.ListOwnerReviews(ctx)
	if err != nil {
		return nil, err
	}
	out := api.BoardAttention{
		Sessions: []api.BoardAttentionSession{}, Automations: []api.BoardAttentionAutomation{},
		Results: make([]api.AutomationResultSummary, 0, len(results)), TaskReviews: make([]api.BoardAttentionTaskReview, 0, len(reviews)),
	}
	for _, r := range results {
		out.Results = append(out.Results, api.AutomationResultSummary{
			RunId: r.RunID, BotId: r.BotID, AutomationId: r.AutomationID, AutomationName: r.AutomationName,
			SessionId: r.SessionID, Outcome: api.AutomationResultSummaryOutcome(r.Outcome), Summary: r.Summary,
			CreatedAt: r.CreatedAt, Read: r.Read, RunnerId: r.RunnerID, Cwd: r.Cwd,
		})
	}
	for _, r := range reviews {
		out.TaskReviews = append(out.TaskReviews, api.BoardAttentionTaskReview{
			Key: task.FormatKey(r.Number), Title: r.Title, AssigneeBotId: r.AssigneeBotID,
		})
	}
	modelChecks := make(map[engine.ModelRef]bool)
	for _, bot := range bots {
		main, err := h.Sessions.ExistingMain(ctx, bot.ID)
		if err != nil {
			return nil, err
		}
		if main != nil {
			out.Sessions = appendBoardSession(out.Sessions, bot.ID, *main)
		}
		sessions, err := h.Sessions.List(ctx, bot.ID)
		if err != nil {
			return nil, err
		}
		for _, session := range sessions {
			_, automationRun := automation.SessionAutomation(session.ChannelKey)
			// Cross-Bot child sessions appear in both lists. Count them for their own Bot.
			if session.BotID == "" && !automationRun {
				out.Sessions = appendBoardSession(out.Sessions, bot.ID, session)
			}
		}
		list, err := h.Automations.List(ctx, bot.ID)
		if err != nil {
			return nil, err
		}
		for _, a := range list {
			item := api.BoardAttentionAutomation{Id: a.ID, BotId: bot.ID, Name: a.Name}
			if a.Enabled {
				item.Setup, err = h.automationNeedsModel(ctx, bot, a, modelChecks)
				if err != nil {
					return nil, err
				}
			}
			if item.Setup && a.Session == "session" && a.TargetSessionID != 0 {
				item.TargetSessionId = new(a.TargetSessionID)
			}
			out.Automations = append(out.Automations, item)
		}
	}
	return api.GetBoardAttention200JSONResponse(out), nil
}

func appendBoardSession(sessions []api.BoardAttentionSession, botID string, c session.Session) []api.BoardAttentionSession {
	if c.ArchivedAt != 0 {
		return sessions
	}
	item := api.BoardAttentionSession{Id: c.ID, BotId: botID, Title: c.Title, Main: c.ChannelKey == session.MainKey, Busy: c.Busy}
	if q := c.Question; q != nil && q.Status == "open" {
		item.Question = &api.BoardAttentionQuestion{Id: q.ID, Text: q.Text, Options: q.Options}
	}
	if item.Busy || item.Question != nil {
		sessions = append(sessions, item)
	}
	return sessions
}

func (h *handler) automationNeedsModel(ctx context.Context, bot bot.Bot, a automation.Automation, checked map[engine.ModelRef]bool) (bool, error) {
	primary := engine.ModelRef{Provider: bot.Provider, ModelID: bot.ModelID}
	if a.Session == "session" && a.TargetSessionID != 0 {
		target, err := h.Sessions.Get(ctx, bot.ID, a.TargetSessionID)
		if errors.Is(err, session.ErrNotFound) {
			// A missing target needs a new binding, not a model.
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if target.Settings.ModelID != "" {
			primary = engine.ModelRef{Provider: target.Settings.Provider, ModelID: target.Settings.ModelID}
		}
	}
	fallback := engine.ModelRef{Provider: bot.FallbackProvider, ModelID: bot.FallbackModelID}
	for _, model := range [...]engine.ModelRef{primary, fallback} {
		if model.ModelID == "" {
			continue
		}
		usable, ok := checked[model]
		if !ok {
			err := h.Settings.CheckModel(ctx, model.Provider, model.ModelID)
			usable = err == nil || errors.Is(err, engine.ErrModelsPending)
			if !usable && !errors.Is(err, settings.ErrInvalid) {
				return false, err
			}
			checked[model] = usable
		}
		if usable {
			return false, nil
		}
	}
	return true, nil
}
