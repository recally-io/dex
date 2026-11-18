package app_test

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/db"
)

func TestUserWorkCollectsAutomationsAcrossBots(t *testing.T) {
	t.Parallel()
	// Given two bots, each with an active automation.
	dir := t.TempDir()
	d := startDexWith(t, app.Config{DataDir: dir, AutomationCheck: time.Hour})
	d.setModel(t, "echo")
	var bot struct {
		ID string `json:"id"`
	}
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Other bot", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, &bot, http.StatusCreated)
	for _, id := range []string{"default", bot.ID} {
		var created automationInfo
		d.must(t, http.MethodPost, "/api/agents/"+id+"/automations", map[string]any{
			"name": "Daily check", "prompt": "Check", "schedule": map[string]string{"kind": "daily", "time": "09:00"},
			"timeZone": "UTC", "session": "new", "enabled": true,
		}, &created, http.StatusCreated)
	}

	// When the owner requests Work.
	var result struct {
		Automations []struct {
			AgentID    string `json:"agentId"`
			Automation struct {
				Name string `json:"name"`
			} `json:"automation"`
			Setup bool `json:"setup"`
		} `json:"automations"`
	}
	d.must(t, http.MethodGet, "/api/work", nil, &result)

	// Then both bots' automations are visible.
	seen := map[string]bool{}
	for _, a := range result.Automations {
		if a.Automation.Name != "Daily check" {
			t.Fatalf("automation unexpected: %+v", a)
		}
		seen[a.AgentID] = true
	}
	if len(result.Automations) != 2 || !seen["default"] || !seen[bot.ID] {
		t.Fatalf("automations across bots: %+v", result.Automations)
	}

	// When one bot loses its model, its active automation needs setup.
	d.must(t, http.MethodPut, "/api/agents/"+bot.ID, map[string]string{"name": "Other bot", "provider": "", "modelId": "", "thinkingLevel": "off"}, nil)
	result.Automations = nil
	d.must(t, http.MethodGet, "/api/work", nil, &result)
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
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(t.Context(), "UPDATE agents SET model_provider = 'missing', model_id = 'missing' WHERE id = ?", bot.ID); err != nil {
		t.Fatal(err)
	}
	result.Automations = nil
	d.must(t, http.MethodGet, "/api/work", nil, &result)
	for _, a := range result.Automations {
		if a.Setup != (a.AgentID == bot.ID) {
			t.Fatalf("unavailable model setup state: %+v", a)
		}
	}
	d.must(t, http.MethodPut, "/api/agents/"+bot.ID, map[string]string{"name": "Other bot", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, nil)
	result.Automations = nil
	d.must(t, http.MethodGet, "/api/work", nil, &result)
	for _, a := range result.Automations {
		if a.Setup {
			t.Fatalf("setup remained after fixing the model: %+v", a)
		}
	}
}

type workQuestions struct {
	Sessions []struct {
		AgentID string `json:"agentId"`
		Chat    struct {
			ID       int64         `json:"id"`
			Main     bool          `json:"main"`
			Question *questionView `json:"question"`
		} `json:"chat"`
	} `json:"sessions"`
}

func (d *dex) openQuestions(t *testing.T) map[int64]string {
	t.Helper()
	var work workQuestions
	d.must(t, http.MethodGet, "/api/work", nil, &work)
	open := map[int64]string{}
	for _, s := range work.Sessions {
		if q := s.Chat.Question; q != nil && q.Status == "open" {
			open[s.Chat.ID] = s.AgentID + ":" + q.ID
		}
	}
	return open
}

func TestUserSeesOpenQuestionInWorkAndNotAfterAnswer(t *testing.T) {
	t.Parallel()
	// Given a Main session and a chat of one bot, each with an open question.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "tool")
	var main chatInfo
	d.must(t, http.MethodGet, "/api/agents/default/chats/main", nil, &main)
	d.send(t, main.ID, askJSON)
	chatID := d.newChat(t, askJSON)
	mainQ := d.awaitQuestion(t, main.ID, "open")
	chatQ := d.awaitQuestion(t, chatID, "open")
	d.awaitIdle(t, main.ID)
	d.awaitIdle(t, chatID)

	// Then Work lists both sessions with their open question.
	got := d.openQuestions(t)
	if len(got) != 2 || got[main.ID] != "default:"+mainQ.ID || got[chatID] != "default:"+chatQ.ID {
		t.Fatalf("open questions in Work: %+v", got)
	}

	// When the user answers the Main question, then Work no longer lists it and still lists the chat.
	d.setModel(t, "echo")
	d.answer(t, main.ID, mainQ.ID, "Investigate only", http.StatusAccepted)
	eventually(t, "Main question leaves Work", func() (bool, any) {
		got := d.openQuestions(t)
		return len(got) == 1 && got[chatID] == "default:"+chatQ.ID, got
	})

	// When the user answers the chat question, then Work lists no question.
	d.answer(t, chatID, chatQ.ID, "Investigate only", http.StatusAccepted)
	eventually(t, "chat question leaves Work", func() (bool, any) {
		got := d.openQuestions(t)
		return len(got) == 0, got
	})
}
