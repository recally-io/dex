package app_test

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/db"
)

func TestUserWorkCollectsAutomationsAcrossBots(t *testing.T) {
	// Given two bots, each with an active automation.
	dir := t.TempDir()
	d := startDexWith(t, app.Config{DataDir: dir, AutomationCheck: time.Hour})
	d.setModel(t, "echo")
	var bot struct {
		ID string `json:"id"`
	}
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Other bot", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &bot, http.StatusCreated)
	ids := map[string]string{}
	for _, id := range []string{"default", bot.ID} {
		var created automationInfo
		d.must(t, http.MethodPost, "/api/agents/"+id+"/automations", map[string]any{
			"name": "Daily check", "prompt": "Check", "schedule": map[string]string{"kind": "daily", "time": "09:00"},
			"timeZone": "UTC", "session": "new", "enabled": true,
		}, &created, http.StatusCreated)
		ids[id] = created.ID
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
			Runs      []struct {
				ID string `json:"id"`
			} `json:"runs"`
			Setup bool `json:"setup"`
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

	// A saved model can become unavailable after its provider is removed.
	conn, err := db.Open(t.Context(), filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), "UPDATE agents SET model_provider = 'missing', model_id = 'missing' WHERE id = ?", bot.ID); err != nil {
		t.Fatal(err)
	}
	result.Automations = nil
	d.must(t, http.MethodGet, fmt.Sprintf("/api/work?from=%d&to=%d", now.UnixMilli(), now.Add(7*24*time.Hour).UnixMilli()), nil, &result)
	for _, a := range result.Automations {
		if a.Setup != (a.AgentID == bot.ID) {
			t.Fatalf("unavailable model setup state: %+v", a)
		}
	}
	d.must(t, http.MethodPut, "/api/agents/"+bot.ID, map[string]string{"name": "Other bot", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, nil)
	result.Automations = nil
	d.must(t, http.MethodGet, fmt.Sprintf("/api/work?from=%d&to=%d", now.UnixMilli(), now.Add(7*24*time.Hour).UnixMilli()), nil, &result)
	for _, a := range result.Automations {
		if a.Setup {
			t.Fatalf("setup remained after fixing the model: %+v", a)
		}
	}

	// Only runs in the requested interval appear in Work.
	for _, r := range []struct {
		id string
		at int64
	}{{"inside", now.Add(-time.Hour).UnixMilli()}, {"outside", now.Add(-48 * time.Hour).UnixMilli()}} {
		if _, err := conn.ExecContext(t.Context(), `INSERT INTO automation_runs(id,automation_id,status,created_at) VALUES (?,?,'skipped',?)`, r.id, ids["default"], r.at); err != nil {
			t.Fatal(err)
		}
	}
	result.Automations = nil
	d.must(t, http.MethodGet, fmt.Sprintf("/api/work?from=%d&to=%d", now.Add(-24*time.Hour).UnixMilli(), now.Add(time.Hour).UnixMilli()), nil, &result)
	for _, a := range result.Automations {
		if a.AgentID == "default" && (len(a.Runs) != 1 || a.Runs[0].ID != "inside") {
			t.Fatalf("work range: %+v", a.Runs)
		}
	}
}
