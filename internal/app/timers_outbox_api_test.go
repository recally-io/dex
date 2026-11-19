package app_test

import (
	"fmt"
	"net/http"
	"testing"
)

func TestUserInspectsTimersAndOutboxViaAPI(t *testing.T) {
	t.Parallel()
	// Given a chat where the agent schedules a follow-up timer via tool call.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "tool")
	chatID := d.newSession(t).ID
	d.send(t, chatID, `timer_set {"delaySeconds":300,"prompt":"check build status"}`)
	d.waitSettled(t, chatID)

	// When the user queries timers and outbox for the chat.
	var timersResp struct {
		Timers []struct {
			ID         string `json:"id"`
			Prompt     string `json:"prompt"`
			State      string `json:"state"`
			ChainDepth int    `json:"chainDepth"`
		} `json:"timers"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d/timers", chatID), nil, &timersResp)
	if len(timersResp.Timers) != 1 || timersResp.Timers[0].State != "scheduled" || timersResp.Timers[0].Prompt != "check build status" {
		t.Fatalf("unexpected timers: %+v", timersResp.Timers)
	}

	var outboxResp struct {
		Messages []struct {
			ID     string `json:"id"`
			Kind   string `json:"kind"`
			Status string `json:"status"`
		} `json:"messages"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d/outbox", chatID), nil, &outboxResp)
	if len(outboxResp.Messages) == 0 || outboxResp.Messages[0].Kind != "reply" || outboxResp.Messages[0].Status != "delivered" {
		t.Fatalf("unexpected outbox: %+v", outboxResp.Messages)
	}

	// When the user cancels the scheduled timer via DELETE.
	var cancelled struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	d.must(t, http.MethodDelete, fmt.Sprintf("/api/agents/default/chats/%d/timers/%s", chatID, timersResp.Timers[0].ID), nil, &cancelled)
	if cancelled.State != "cancelled" {
		t.Fatalf("expected cancelled timer, got %+v", cancelled)
	}

	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d/timers", chatID), nil, &timersResp)
	if len(timersResp.Timers) != 1 || timersResp.Timers[0].State != "cancelled" {
		t.Fatalf("expected cancelled state in list, got %+v", timersResp.Timers)
	}
}
