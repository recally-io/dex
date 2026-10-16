package app_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/settings"
)

func TestUserNamesBot(t *testing.T) {
	// Given a bot with saved model settings.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "echo")
	var saved struct {
		Agent settings.Agent `json:"agent"`
	}
	d.must(t, http.MethodGet, "/api/settings", nil, &saved)
	input := saved.Agent
	input.Name = "  Helper  "
	// When the user changes its name, the API trims and saves it.
	d.must(t, http.MethodPut, "/api/settings/agent", input, nil)
	d.must(t, http.MethodGet, "/api/settings", nil, &saved)
	if saved.Agent.Name != "Helper" || saved.Agent.ModelID != "echo" {
		t.Fatalf("saved agent=%+v", saved.Agent)
	}
	for _, name := range []string{"", "  ", strings.Repeat("x", 41)} {
		input.Name = name
		d.must(t, http.MethodPut, "/api/settings/agent", input, nil, http.StatusBadRequest)
	}
	input.Name = strings.Repeat("助", 40)
	d.must(t, http.MethodPut, "/api/settings/agent", input, nil)
	d.stop()
	// Then the bot keeps the name after a restart, including non-ASCII characters.
	d = startDex(t, dir, "")
	d.must(t, http.MethodGet, "/api/settings", nil, &saved)
	if saved.Agent.Name != input.Name {
		t.Fatalf("name after restart=%q", saved.Agent.Name)
	}
}
