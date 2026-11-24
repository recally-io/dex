package app_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestUserDescribesExecutionEnvironments(t *testing.T) {
	// Given a saved Runner without a description.
	const settingsPath = "/api/settings/environments"
	type environment struct {
		ID          string  `json:"id"`
		Name        string  `json:"name"`
		Kind        string  `json:"kind"`
		Description *string `json:"desc"`
		Status      string  `json:"status"`
		Default     bool    `json:"default"`
		Runner      *struct {
			Online bool `json:"online"`
		} `json:"runner"`
	}
	data := t.TempDir()
	d := startDex(t, data, "")
	var runner environment
	body := map[string]any{"name": "Build runner", "kind": "runner"}
	d.must(t, http.MethodPost, settingsPath, body, &runner, http.StatusCreated)
	if runner.Description != nil || runner.Runner == nil || runner.Runner.Online {
		t.Fatalf("new Runner without description: %+v", runner)
	}
	chat := d.newSession(t).ID
	assertListed := func(want *string) {
		t.Helper()
		var settings, sessions, tool struct {
			Environments []environment `json:"environments"`
		}
		d.must(t, http.MethodGet, settingsPath, nil, &settings)
		d.must(t, http.MethodGet, "/api/agents/default/environments", nil, &sessions)
		taskToolJSON(t, d, chat, "environment_list", map[string]any{}, &tool)
		for path, list := range map[string][]environment{"Settings": settings.Environments, "sessions": sessions.Environments, "tool": tool.Environments} {
			found := false
			for _, env := range list {
				if env.ID == "local" && (env.Description != nil || !env.Default) {
					t.Fatalf("%s local environment changed: %+v", path, env)
				}
				if env.ID != runner.ID {
					continue
				}
				found = true
				if env.Name != runner.Name || env.Kind != "runner" || (path == "Settings" && (env.Runner == nil || env.Runner.Online)) || (path != "Settings" && env.Status != "offline") {
					t.Fatalf("%s Runner changed: %+v", path, env)
				}
				assertEnvironmentDescription(t, path, env.Description, want)
			}
			if !found {
				t.Fatalf("%s did not list Runner %s", path, runner.ID)
			}
		}
	}
	assertListed(nil)

	// When the owner creates another Runner with a description, it is returned as data.
	const initial = "Use for macOS builds"
	var created environment
	d.must(t, http.MethodPost, settingsPath, map[string]any{"name": "Second runner", "kind": "runner", "desc": initial}, &created, http.StatusCreated)
	assertEnvironmentDescription(t, "create", created.Description, new(initial))
	var list struct {
		Environments []environment `json:"environments"`
	}
	d.must(t, http.MethodGet, settingsPath, nil, &list)
	if len(list.Environments) != 2 || list.Environments[1].ID != created.ID {
		t.Fatalf("created environment not listed: %+v", list.Environments)
	}
	assertEnvironmentDescription(t, "create then list", list.Environments[1].Description, new(initial))

	// When the owner edits the original Runner, then the text survives a restart.
	const edited = "macOS builds\n<strong>UI checks</strong>"
	path := settingsPath + "/" + runner.ID
	body["desc"] = "  " + edited + "  "
	d.must(t, http.MethodPut, path, body, nil)
	d.stop()
	d = startDex(t, data, "")
	assertListed(new(edited))

	// An old client can omit the field. An oversized value does not replace it.
	delete(body, "desc")
	d.must(t, http.MethodPut, path, body, nil)
	body["desc"] = strings.Repeat("界", 501)
	d.must(t, http.MethodPut, path, body, nil, http.StatusBadRequest)
	assertListed(new(edited))

	// When the owner clears the description, then the optional field is absent again.
	body["desc"] = ""
	d.must(t, http.MethodPut, path, body, nil)
	assertListed(nil)
}

func assertEnvironmentDescription(t *testing.T, source string, got, want *string) {
	t.Helper()
	if got == nil && want == nil {
		return
	}
	if got == nil {
		t.Fatalf("%s: desc missing, want %q", source, *want)
	}
	if want == nil {
		t.Fatalf("%s: desc = %q, want omission", source, *got)
	}
	if *got != *want {
		t.Fatalf("%s: desc = %q, want %q", source, *got, *want)
	}
}
