package app_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
)

func TestUserWorkCollectsAutomationsAcrossBots(t *testing.T) {
	// Given two bots, each with an active automation.
	d := startDexWith(t, app.Config{DataDir: t.TempDir(), AutomationCheck: time.Hour})
	d.setModel(t, "echo")
	var bot struct {
		ID string `json:"id"`
	}
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Other bot", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &bot, http.StatusCreated)
	for _, id := range []string{"default", bot.ID} {
		d.must(t, http.MethodPost, "/api/agents/"+id+"/automations", map[string]any{
			"name": "Daily check", "prompt": "Check", "schedule": map[string]string{"kind": "daily", "time": "09:00"},
			"timeZone": "UTC", "session": "new", "enabled": true,
		}, nil, http.StatusCreated)
	}

	// When the owner requests Work for the next seven days.
	now := time.Now()
	var result struct {
		Automations []struct {
			AgentID    string `json:"agentId"`
			Automation struct {
				Name string `json:"name"`
			} `json:"automation"`
			Scheduled []int64 `json:"scheduled"`
			Setup     bool    `json:"setup"`
		} `json:"automations"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/work?from=%d&to=%d", now.UnixMilli(), now.Add(7*24*time.Hour).UnixMilli()), nil, &result)

	// Then both bots' automations and their scheduled times are visible.
	seen := map[string]bool{}
	for _, a := range result.Automations {
		if a.Automation.Name != "Daily check" || len(a.Scheduled) != 7 {
			t.Fatalf("automation missing schedule: %+v", a)
		}
		seen[a.AgentID] = true
	}
	if len(result.Automations) != 2 || !seen["default"] || !seen[bot.ID] {
		t.Fatalf("automations across bots: %+v", result.Automations)
	}

	// When one bot loses its model, its active automation needs setup.
	d.must(t, http.MethodPut, "/api/agents/"+bot.ID, map[string]string{"name": "Other bot", "provider": "", "modelId": "", "thinkingLevel": "off"}, nil)
	result.Automations = nil
	d.must(t, http.MethodGet, fmt.Sprintf("/api/work?from=%d&to=%d", now.UnixMilli(), now.Add(7*24*time.Hour).UnixMilli()), nil, &result)
	for _, a := range result.Automations {
		if a.Setup != (a.AgentID == bot.ID) {
			t.Fatalf("unexpected setup state: %+v", a)
		}
	}
}
