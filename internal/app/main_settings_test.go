package app_test

import (
	"fmt"
	"net/http"
	"testing"
)

func TestUserMainModelAppliesToWebAndChannelInput(t *testing.T) {
	t.Parallel()
	// Given Main on the agent model and a paired channel.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	id := d.mainChat(t)
	if got := taskTurn(t, d, id, "one"); got != "echo: one" {
		t.Fatal(got)
	}
	// When the user picks another model and thinking level for Main.
	url := fmt.Sprintf("/api/agents/default/chats/%d/settings", id)
	d.must(t, http.MethodPut, url, map[string]string{"provider": "faux", "modelId": "think", "thinkingLevel": "high"}, nil)
	// Then web input and the next channel input both use them.
	if got := taskTurn(t, d, id, "two"); got != "think high: two" {
		t.Fatal(got)
	}
	f.message(1, 42, 7, "three")
	if got := f.next(t); got.Text != "think high: three" {
		t.Fatalf("channel answer: %+v", got)
	}
	// And the setting is part of Main's view.
	var view struct {
		Settings chatSettings `json:"settings"`
	}
	d.must(t, http.MethodGet, "/api/agents/default/chats/main", nil, &view)
	if view.Settings.ModelID != "think" || view.Settings.ThinkingLevel != "high" {
		t.Fatalf("settings: %+v", view.Settings)
	}
}
