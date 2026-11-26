package app_test

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/db"
)

func TestUserSeesAutomationSetupAcrossBotsOnBoard(t *testing.T) {
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

	// When the Owner requests Board attention.
	var result api.BoardAttention
	d.must(t, http.MethodGet, "/api/board/attention", nil, &result)

	// Then both bots' automations are visible.
	seen := map[string]bool{}
	for _, a := range result.Automations {
		if a.Name != "Daily check" {
			t.Fatalf("automation unexpected: %+v", a)
		}
		seen[a.AgentId] = true
	}
	if len(result.Automations) != 2 || !seen["default"] || !seen[bot.ID] {
		t.Fatalf("automations across bots: %+v", result.Automations)
	}

	// When one bot loses its model, its active automation needs setup.
	d.must(t, http.MethodPut, "/api/agents/"+bot.ID, map[string]string{"name": "Other bot", "provider": "", "modelId": "", "thinkingLevel": "off"}, nil)
	result.Automations = nil
	d.must(t, http.MethodGet, "/api/board/attention", nil, &result)
	for _, a := range result.Automations {
		if a.Setup != (a.AgentId == bot.ID) {
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
	d.must(t, http.MethodGet, "/api/board/attention", nil, &result)
	for _, a := range result.Automations {
		if a.Setup != (a.AgentId == bot.ID) {
			t.Fatalf("unavailable model setup state: %+v", a)
		}
	}
	d.must(t, http.MethodPut, "/api/agents/"+bot.ID, map[string]string{"name": "Other bot", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, nil)
	result.Automations = nil
	d.must(t, http.MethodGet, "/api/board/attention", nil, &result)
	for _, a := range result.Automations {
		if a.Setup {
			t.Fatalf("setup remained after fixing the model: %+v", a)
		}
	}
}

func TestUserSeesSetupBasedOnBoundSessionModel(t *testing.T) {
	t.Parallel()
	// Given an enabled Automation bound to a session with its own model.
	dir := t.TempDir()
	d := startDexWith(t, app.Config{DataDir: dir, AutomationCheck: time.Hour})
	d.setModel(t, "echo")
	session := d.newSession(t)
	d.must(t, http.MethodPut, fmt.Sprintf("/api/agents/default/sessions/%d/settings", session.ID),
		map[string]string{"provider": "faux", "modelId": "shout"}, nil)
	d.must(t, http.MethodPost, "/api/agents/default/automations", map[string]any{
		"name": "Bound check", "prompt": "Check", "schedule": map[string]string{"kind": "daily", "time": "09:00"},
		"timeZone": "UTC", "session": "session", "targetSessionId": session.ID, "enabled": true,
	}, nil, http.StatusCreated)

	assertSetup := func(want bool) {
		t.Helper()
		var board api.BoardAttention
		d.must(t, http.MethodGet, "/api/board/attention", nil, &board)
		if len(board.Automations) != 1 || board.Automations[0].Setup != want {
			t.Errorf("bound Automation setup: got %+v, want setup=%t", board.Automations, want)
		}
	}

	// When the Bot default is cleared, the valid session override needs no setup.
	d.must(t, http.MethodPut, "/api/agents/default", map[string]string{"name": "Dex", "provider": "", "modelId": "", "thinkingLevel": "off"}, nil)
	assertSetup(false)

	// A missing provider for the Bot default also leaves the session override usable.
	conn, err := db.Open(t.Context(), filepath.Join(dir, "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(t.Context(), "UPDATE agents SET model_provider = 'missing', model_id = 'missing' WHERE id = 'default'"); err != nil {
		t.Fatal(err)
	}
	assertSetup(false)

	// When a session override becomes unavailable, a valid Bot default does not replace it.
	d.setModel(t, "echo")
	if _, err := conn.ExecContext(t.Context(), "UPDATE sessions SET model_provider = 'missing', model_id = 'missing' WHERE id = ?", session.ID); err != nil {
		t.Fatal(err)
	}
	assertSetup(true)

	// The Bot fallback remains available to the bound session.
	d.must(t, http.MethodPut, "/api/agents/default", map[string]string{"name": "Dex", "provider": "faux", "modelId": "echo", "thinkingLevel": "off", "fallbackProvider": "faux", "fallbackModelId": "shout"}, nil)
	assertSetup(false)
}

func (d *dex) openQuestions(t *testing.T) map[int64]string {
	t.Helper()
	var attention api.BoardAttention
	d.must(t, http.MethodGet, "/api/board/attention", nil, &attention)
	open := map[int64]string{}
	for _, s := range attention.Sessions {
		if q := s.Question; q != nil {
			open[s.Id] = s.AgentId + ":" + q.Id
		}
	}
	return open
}

func TestUserSeesOpenQuestionOnBoardUntilAnswered(t *testing.T) {
	t.Parallel()
	// Given a Main session and a session of one bot, each with an open question.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "tool")
	var main sessionListItem
	d.must(t, http.MethodGet, "/api/agents/default/sessions/main", nil, &main)
	d.send(t, main.ID, askJSON)
	sessionID := d.newSessionWithMessage(t, askJSON)
	mainQ := d.awaitQuestion(t, main.ID, "open")
	sessionQ := d.awaitQuestion(t, sessionID, "open")
	d.awaitIdle(t, main.ID)
	d.awaitIdle(t, sessionID)

	// Then Board lists both sessions with their open question.
	got := d.openQuestions(t)
	if len(got) != 2 || got[main.ID] != "default:"+mainQ.ID || got[sessionID] != "default:"+sessionQ.ID {
		t.Fatalf("open questions in Board: %+v", got)
	}

	// When the user answers the Main question, then Board no longer lists it and still lists the session.
	d.setModel(t, "echo")
	d.answer(t, main.ID, mainQ.ID, "Investigate only", http.StatusAccepted)
	eventually(t, "Main question leaves Board", func() (bool, any) {
		got := d.openQuestions(t)
		return len(got) == 1 && got[sessionID] == "default:"+sessionQ.ID, got
	})

	// When the user answers the session question, then Board lists no question.
	d.answer(t, sessionID, sessionQ.ID, "Investigate only", http.StatusAccepted)
	eventually(t, "session question leaves Board", func() (bool, any) {
		got := d.openQuestions(t)
		return len(got) == 0, got
	})
}
