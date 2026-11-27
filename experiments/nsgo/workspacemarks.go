package main

import (
	"context"
	"fmt"
	"net/http"
)

type workspaceMarks struct {
	Needs, Working map[string]bool
	Count          int
	ProviderErrors int
}

func loadWorkspaceMarks(ctx context.Context) (workspaceMarks, error) {
	var attention struct {
		Sessions []struct {
			BotID    string    `json:"botId"`
			Busy     bool      `json:"busy"`
			Question *struct{} `json:"question"`
		} `json:"sessions"`
		Automations []struct {
			BotID string `json:"botId"`
			Setup bool   `json:"setup"`
		} `json:"automations"`
		Results []struct {
			BotID string `json:"botId"`
		} `json:"results"`
		TaskReviews []struct {
			AssigneeBotID string `json:"assigneeBotId"`
		} `json:"taskReviews"`
	}
	var held struct {
		Total int `json:"total"`
	}
	var settings struct {
		Providers []struct {
			Error string `json:"error"`
		} `json:"providers"`
	}
	var bots struct {
		Bots []struct {
			ID       string `json:"id"`
			Activity struct {
				ActiveSessions int `json:"activeSessions"`
			} `json:"activity"`
		} `json:"bots"`
	}
	for _, endpoint := range []struct {
		path string
		out  any
	}{
		{"/api/board/attention", &attention},
		{"/api/sources/held-events", &held},
		{"/api/settings", &settings},
		{"/api/bots", &bots},
	} {
		if err := requestJSON(ctx, http.MethodGet, endpoint.path, nil, endpoint.out); err != nil {
			return workspaceMarks{}, fmt.Errorf("load workspace marks from %s: %w", endpoint.path, err)
		}
	}

	// Held events have no assigned Bot. The total can exceed the number returned.
	marks := workspaceMarks{Needs: make(map[string]bool), Working: make(map[string]bool), Count: held.Total}
	needs := func(botID string) {
		marks.Count++
		if botID != "" {
			marks.Needs[botID] = true
		}
	}
	for _, session := range attention.Sessions {
		if session.Question != nil {
			needs(session.BotID)
		}
		if session.Busy {
			marks.Working[session.BotID] = true
		}
	}
	for _, automation := range attention.Automations {
		if automation.Setup {
			needs(automation.BotID)
		}
	}
	for _, result := range attention.Results {
		needs(result.BotID)
	}
	for _, review := range attention.TaskReviews {
		needs(review.AssigneeBotID)
	}
	for _, bot := range bots.Bots {
		if bot.Activity.ActiveSessions > 0 {
			marks.Working[bot.ID] = true
		}
	}
	// A Bot that needs attention does not show a working mark.
	for botID := range marks.Needs {
		delete(marks.Working, botID)
	}
	for _, provider := range settings.Providers {
		if provider.Error != "" {
			marks.ProviderErrors++
		}
	}
	return marks, nil
}
