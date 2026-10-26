package app_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/agent"
)

func TestUserNamesBot(t *testing.T) {
	t.Parallel()
	// Given a bot with saved model settings.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "echo")
	var saved agent.Agent
	d.must(t, http.MethodGet, "/api/agents/default", nil, &saved)
	input := saved
	input.Name = "  Helper  "
	// When the user changes its name, the API trims and saves it.
	d.must(t, http.MethodPut, "/api/agents/default", input, nil)
	d.must(t, http.MethodGet, "/api/agents/default", nil, &saved)
	if saved.Name != "Helper" || saved.ModelID != "echo" {
		t.Fatalf("saved agent=%+v", saved)
	}
	for _, name := range []string{"", "  ", strings.Repeat("x", 41)} {
		input.Name = name
		d.must(t, http.MethodPut, "/api/agents/default", input, nil, http.StatusBadRequest)
	}
	input.Name = strings.Repeat("助", 40)
	d.must(t, http.MethodPut, "/api/agents/default", input, nil)
	d.stop()
	// Then the bot keeps the name after a restart, including non-ASCII characters.
	d = startDex(t, dir, "")
	d.must(t, http.MethodGet, "/api/agents/default", nil, &saved)
	if saved.Name != input.Name {
		t.Fatalf("name after restart=%q", saved.Name)
	}
}
