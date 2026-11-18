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

func TestUserConfiguresBotDefaultsAndCreatesReadyBots(t *testing.T) {
	t.Parallel()
	// Given a fresh installation with E2B environments and no configured default bot model.
	fake := execenvtest.NewFakeE2B(t, "e2b_live_secret")
	d := startDexWith(t, app.Config{
		DataDir:    t.TempDir(),
		E2BAPIURL:  fake.APIURL(),
		E2BEnvdURL: fake.EnvdURL(),
	})

	var initialSettings api.Settings
	d.must(t, http.MethodGet, "/api/settings", nil, &initialSettings)
	if initialSettings.Defaults.ModelID != "" || initialSettings.Defaults.ThinkingLevel != "off" {
		t.Fatalf("unexpected initial defaults: %+v", initialSettings.Defaults)
	}
	var initialDefault api.Agent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &initialDefault)
	if initialDefault.ModelID != "" {
		t.Fatalf("expected unconfigured default bot, got %+v", initialDefault)
	}

	var envA, envB apiEnvironment
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name":   "Default Sandbox",
		"kind":   "e2b",
		"config": map[string]any{"template": "base", "timeoutSeconds": 300},
		"secret": "e2b_live_secret",
	}, &envA, http.StatusCreated)
	d.must(t, http.MethodPost, "/api/settings/environments", map[string]any{
		"name":   "Disposable Default",
		"kind":   "e2b",
		"config": map[string]any{"template": "base", "timeoutSeconds": 300},
		"secret": "e2b_live_secret",
	}, &envB, http.StatusCreated)

	// When the owner saves global bot defaults, the unconfigured default bot is backfilled.
	wantDefaults := api.BotDefaults{
		Provider:         "faux",
		ModelID:          "echo",
		ThinkingLevel:    "off",
		FallbackProvider: "faux",
		FallbackModelID:  "shout",
		EnvironmentID:    envA.ID,
	}
	var savedDefaults api.BotDefaults
	d.must(t, http.MethodPut, "/api/settings/defaults", wantDefaults, &savedDefaults)
	if savedDefaults != wantDefaults {
		t.Fatalf("saved defaults=%+v want %+v", savedDefaults, wantDefaults)
	}

	var settingsAfter api.Settings
	d.must(t, http.MethodGet, "/api/settings", nil, &settingsAfter)
	if settingsAfter.Defaults != wantDefaults {
		t.Fatalf("GET /api/settings defaults=%+v want %+v", settingsAfter.Defaults, wantDefaults)
	}

	var backfilled api.Agent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &backfilled)
	if backfilled.Provider != "faux" || backfilled.ModelID != "echo" ||
		backfilled.FallbackProvider != "faux" || backfilled.FallbackModelID != "shout" ||
		backfilled.EnvironmentID != envA.ID {
		t.Fatalf("default bot was not backfilled: %+v", backfilled)
	}

	// Once default has a primary model, updating global defaults leaves it alone.
	d.must(t, http.MethodPut, "/api/agents/default", map[string]string{
		"name":          "Dex",
		"provider":      "faux",
		"modelId":       "tool",
		"thinkingLevel": "off",
		"environmentId": "",
	}, nil)
	d.must(t, http.MethodPut, "/api/settings/defaults", wantDefaults, nil)
	d.must(t, http.MethodGet, "/api/agents/default", nil, &backfilled)
	if backfilled.ModelID != "tool" || backfilled.EnvironmentID != "" {
		t.Fatalf("configured default bot was overwritten: %+v", backfilled)
	}

	// Creating a bot with only a name inherits defaults and answers immediately.
	var quick api.Agent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Quick Bot"}, &quick, http.StatusCreated)
	if quick.Provider != "faux" || quick.ModelID != "echo" || quick.ThinkingLevel != "off" ||
		quick.FallbackProvider != "faux" || quick.FallbackModelID != "shout" || quick.EnvironmentID != envA.ID {
		t.Fatalf("quick bot did not inherit defaults: %+v", quick)
	}
	var quickMain chatInfo
	d.must(t, http.MethodGet, "/api/agents/"+quick.ID+"/chats/main", nil, &quickMain)
	quickWatch := d.watchPath(t, fmt.Sprintf("/api/agents/%s/chats/%d/events", quick.ID, quickMain.ID))
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/%s/chats/%d/messages", quick.ID, quickMain.ID),
		map[string]string{"id": uuid.NewV7().String(), "text": "ready out of the box"}, nil, http.StatusAccepted)
	quickWatch.until(t, "quick bot answers with default model", answered("echo: ready out of the box"))

	// Creating a bot via bot_create without model fields also inherits global defaults.
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
	if foundToolBot == nil || foundToolBot.ModelID != "echo" || foundToolBot.FallbackModelID != "shout" || foundToolBot.EnvironmentID != envA.ID {
		t.Fatalf("bot_create did not inherit defaults: %+v", listed.Agents)
	}

	// Deleting an unused default environment clears defaults.environmentId.
	nextDefaults := wantDefaults
	nextDefaults.EnvironmentID = envB.ID
	d.must(t, http.MethodPut, "/api/settings/defaults", nextDefaults, nil)
	d.must(t, http.MethodDelete, "/api/settings/environments/"+envB.ID, nil, nil)
	d.must(t, http.MethodGet, "/api/settings", nil, &settingsAfter)
	if settingsAfter.Defaults.EnvironmentID != "" {
		t.Fatalf("deleting environment did not clear default environmentId: %+v", settingsAfter.Defaults)
	}
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

	// Updating the bot's fallback model takes effect on subsequent turns even without a primary model.
	d.must(t, http.MethodPut, "/api/agents/default", map[string]string{
		"name":             "Dex",
		"provider":         "",
		"modelId":          "",
		"thinkingLevel":    "off",
		"fallbackProvider": "faux",
		"fallbackModelId":  "shout",
	}, nil)
	perBotChat := d.newChat(t, "per-bot fallback")
	d.watch(t, perBotChat).until(t, "updated fallback answer", answered("shout: per-bot fallback"))
}
