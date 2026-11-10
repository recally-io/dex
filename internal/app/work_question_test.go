package app_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

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

// openQuestions lists the sessions of Work that wait for an answer, by chat ID.
func (d *dex) openQuestions(t *testing.T) map[int64]string {
	t.Helper()
	var work workQuestions
	now := time.Now()
	d.must(t, http.MethodGet, fmt.Sprintf("/api/work?from=%d&to=%d", now.UnixMilli(), now.Add(7*24*time.Hour).UnixMilli()), nil, &work)
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
