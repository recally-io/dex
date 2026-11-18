package app_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/app"
	"github.com/recally-io/dex/internal/execenv/execenvtest"
)

func TestUserCreatesReadyBotsFromTheDefaultBot(t *testing.T) {
	t.Parallel()
	// Given a fresh installation with an E2B environment.
	fake := execenvtest.NewFakeE2B(t, "e2b_live_secret")
	d := startDexWith(t, app.Config{
		DataDir:    t.TempDir(),
		E2BAPIURL:  fake.APIURL(),
		E2BEnvdURL: fake.EnvdURL(),
	})
	var env apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name":   "Default Sandbox",
		"kind":   "e2b",
		"config": map[string]any{"template": "base", "timeoutSeconds": 300},
		"secret": "e2b_live_secret",
	}, &env, http.StatusCreated)

	// When the owner configures the default bot.
	d.must(t, http.MethodPut, "/api/agents/default", map[string]string{
		"name":             "Dex",
		"provider":         "faux",
		"modelId":          "echo",
		"thinkingLevel":    "off",
		"fallbackProvider": "faux",
		"fallbackModelId":  "shout",
		"environmentId":    env.ID,
	}, nil)

	// Then a bot with only a name copies its model, fallback, and environment and answers at once.
	var quick api.Agent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Quick Bot"}, &quick, http.StatusCreated)
	if quick.Provider != "faux" || quick.ModelID != "echo" || quick.ThinkingLevel != "off" ||
		quick.FallbackProvider != "faux" || quick.FallbackModelID != "shout" || quick.EnvironmentID != env.ID {
		t.Fatalf("quick bot did not copy the default bot: %+v", quick)
	}
	var quickMain chatInfo
	d.must(t, http.MethodGet, "/api/agents/"+quick.ID+"/chats/main", nil, &quickMain)
	quickWatch := d.watchPath(t, fmt.Sprintf("/api/agents/%s/chats/%d/events", quick.ID, quickMain.ID))
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/%s/chats/%d/messages", quick.ID, quickMain.ID),
		map[string]string{"id": uuid.NewV7().String(), "text": "ready out of the box"}, nil, http.StatusAccepted)
	quickWatch.until(t, "quick bot answers with the copied model", answered("echo: ready out of the box"))

	// Explicit fields win over the copy.
	var local api.Agent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{
		"name": "Local Bot", "modelId": "tool", "provider": "faux", "environmentId": "",
	}, &local, http.StatusCreated)
	if local.ModelID != "tool" || local.EnvironmentID != "" || local.FallbackModelID != "shout" {
		t.Fatalf("explicit fields = %+v, want faux/tool on this computer with the copied fallback", local)
	}

	// bot_create without model fields copies the default bot too. The tool session sets its own
	// model on the default bot, so compare with the default bot as it is now.
	toolChat := d.newSession(t)
	_ = taskTool(t, d, toolChat.ID, "bot_create", map[string]string{"name": "Tool Bot"})
	var listed struct {
		Agents []api.Agent `json:"agents"`
	}
	d.must(t, http.MethodGet, "/api/agents", nil, &listed)
	var foundToolBot *api.Agent
	for i := range listed.Agents {
		if listed.Agents[i].Name == "Tool Bot" {
			foundToolBot = &listed.Agents[i]
			break
		}
	}
	var template api.Agent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &template)
	if foundToolBot == nil || foundToolBot.ModelID == "" || foundToolBot.ModelID != template.ModelID ||
		foundToolBot.FallbackModelID != template.FallbackModelID || foundToolBot.EnvironmentID != env.ID {
		t.Fatalf("bot_create did not copy the default bot: %+v", listed.Agents)
	}

	// A change to the default bot does not touch existing bots.
	d.must(t, http.MethodPut, "/api/agents/default", map[string]string{
		"name": "Dex", "provider": "faux", "modelId": "shout", "thinkingLevel": "off",
	}, nil)
	d.must(t, http.MethodGet, "/api/agents/"+quick.ID, nil, &quick)
	if quick.ModelID != "echo" {
		t.Fatalf("existing bot changed with the default bot: %+v", quick)
	}

	// A fallback model needs a model.
	d.must(t, http.MethodPost, "/api/agents", map[string]string{
		"name": "Fallback Only", "modelId": "", "fallbackProvider": "faux", "fallbackModelId": "shout",
	}, nil, http.StatusBadRequest)
}

func TestUserFallsBackWhenPrimaryModelFailsOrDisappears(t *testing.T) {
	t.Parallel()
	// Given a custom provider endpoint whose models list succeeds but completions fail with 503.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"broken-primary"}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/chat/completions"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream overloaded","type":"server_error"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	d := startDex(t, t.TempDir(), "")
	d.must(t, http.MethodPut, "/api/settings/providers/flaky", map[string]any{
		"api":     "openai-completions",
		"name":    "Flaky",
		"baseUrl": upstream.URL + "/v1",
		"apiKey":  "test-key",
	}, nil)
	d.must(t, http.MethodPut, "/api/agents/default", map[string]string{
		"name":             "Dex",
		"provider":         "flaky",
		"modelId":          "broken-primary",
		"thinkingLevel":    "off",
		"fallbackProvider": "faux",
		"fallbackModelId":  "echo",
	}, nil)

	// An active automation counts fallback as usable on /api/work.
	d.must(t, http.MethodPost, "/api/agents/default/automations", map[string]any{
		"name":     "Health check",
		"prompt":   "Check status",
		"schedule": map[string]string{"kind": "daily", "time": "09:00"},
		"timeZone": "UTC",
		"session":  "new",
		"enabled":  true,
	}, nil, http.StatusCreated)

	// When the primary model returns 503 before emitting content, the engine fails over to fallback.
	failoverChat := d.newChat(t, "failover check")
	w := d.watch(t, failoverChat)
	w.until(t, "fallback answer after HTTP 503", answered("echo: failover check"))
	last := w.tr.items[len(w.tr.items)-1]
	if last.Model != "faux/echo" {
		t.Fatalf("assistant model = %q, want faux/echo", last.Model)
	}

	// When the primary provider is deleted, work remains usable and chats still answer via fallback.
	d.must(t, http.MethodDelete, "/api/settings/providers/flaky", nil, nil)

	now := time.Now()
	var work struct {
		Automations []struct {
			AgentID string `json:"agentId"`
			Setup   bool   `json:"setup"`
		} `json:"automations"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/work?from=%d&to=%d", now.UnixMilli(), now.Add(24*time.Hour).UnixMilli()), nil, &work)
	if len(work.Automations) != 1 || work.Automations[0].Setup {
		t.Fatalf("expected automation setup=false while fallback is available, got %+v", work.Automations)
	}

	missingChat := d.newChat(t, "missing provider check")
	d.watch(t, missingChat).until(t, "fallback answer when primary missing", answered("echo: missing provider check"))

	// A new bot does not copy the default bot's removed model, and so no fallback model either.
	var fresh api.Agent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Fresh"}, &fresh, http.StatusCreated)
	if fresh.ModelID != "" || fresh.FallbackModelID != "" {
		t.Fatalf("bot copied a removed model: %+v", fresh)
	}
}
