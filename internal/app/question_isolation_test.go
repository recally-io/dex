package app_test

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUserAnswersQuestionsFromTwoBotsWithTheSameRemoteMessageID(t *testing.T) {
	t.Parallel()
	// Given two Telegram bots whose private chats have the same peer and message IDs.
	first, second := newFakeTelegram(t), newFakeTelegram(t)
	const secondToken = "456:second-secret"
	const remoteID = 7001
	const ownerID int64 = 84
	boundary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bot := first
		if strings.HasPrefix(r.URL.Path, "/bot"+secondToken+"/") {
			r.URL.Path = strings.Replace(r.URL.Path, secondToken, fakeToken, 1)
			bot = second
		}
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			bot.serve(w, r)
			return
		}
		recorder := httptest.NewRecorder()
		bot.serve(recorder, r)
		var response struct {
			OK     bool `json:"ok"`
			Result struct {
				MessageID int64 `json:"message_id"`
			} `json:"result"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || !response.OK {
			t.Errorf("platform send response: %s, %v", recorder.Body.String(), err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		response.Result.MessageID = remoteID
		w.Header().Set("Content-Type", "application/json")
		if err := json.MarshalWrite(w, response); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(boundary.Close)
	data := t.TempDir()
	d := startDex(t, data, boundary.URL)
	d.setModel(t, "tool")
	var other apiAgent
	d.must(t, http.MethodPost, "/api/agents", map[string]string{"name": "Other", "provider": "faux", "modelId": "tool", "thinkingLevel": "off"}, &other, http.StatusCreated)
	bots := []struct {
		bot          *fakeTelegram
		agent, token string
	}{{first, "default", fakeToken}, {second, other.ID, secondToken}}
	conn := sessionDB(t, d)
	for _, entry := range bots {
		base := "/api/agents/" + entry.agent
		d.must(t, http.MethodPut, base+"/channels/telegram", map[string]any{"token": entry.token, "allowedUsers": []int64{ownerID}}, nil)
		entry.bot.message(1, ownerID, ownerID, askJSON)
		if sent := entry.bot.next(t); !strings.Contains(sent.Text, "Reply to this message") {
			t.Fatalf("question reply: %+v", sent)
		}
		// Wait for the durable send receipt before quoting its remote message.
		eventually(t, "question delivery recorded", func() (bool, any) {
			var delivered bool
			err := conn.QueryRowContext(t.Context(), `SELECT EXISTS (
				SELECT 1 FROM submissions s JOIN conversations c ON c.id = s.conversation_id
				WHERE c.agent_id = ? AND s.delivered_at IS NOT NULL
			)`, entry.agent).Scan(&delivered)
			return err == nil && delivered, fmt.Sprintf("delivered=%t err=%v", delivered, err)
		})
		d.must(t, http.MethodPut, base, map[string]string{"name": "Bot", "provider": "faux", "modelId": "echo", "thinkingLevel": "off"}, nil)
	}

	// When each owner quotes their own bot's question after a restart.
	d.stop()
	d = startDex(t, data, boundary.URL)
	for _, entry := range bots {
		entry.bot.reply(2, ownerID, ownerID, "Investigate only", remoteID)
		reply := entry.bot.next(t).Text
		// Then both inputs answer their own questions instead of becoming plain messages.
		if !strings.Contains(reply, "echo: Answer to: How should I handle the banner flicker?\nInvestigate only") {
			t.Errorf("agent %s lost its quoted question: %q", entry.agent, reply)
		}
		var main struct {
			Question *questionView `json:"question"`
		}
		d.must(t, http.MethodGet, "/api/agents/"+entry.agent+"/chats/main", nil, &main)
		if main.Question != nil {
			t.Errorf("agent %s still waits for an answer: %+v", entry.agent, main.Question)
		}
	}
}
