package app_test

import (
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"uuid"
)

type questionView struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Options []string `json:"options"`
	Status  string   `json:"status"`
	Answer  string   `json:"answer"`
}

func (d *dex) question(t *testing.T, sessionID int64) *questionView {
	t.Helper()
	var c struct {
		Question *questionView `json:"question"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/bots/default/sessions/%d", sessionID), nil, &c)
	return c.Question
}

// awaitQuestion polls the session until it shows a question with the status.
func (d *dex) awaitQuestion(t *testing.T, sessionID int64, status string) *questionView {
	t.Helper()
	var q *questionView
	eventually(t, "question "+status, func() (bool, any) {
		q = d.question(t, sessionID)
		return q != nil && q.Status == status, q
	})
	return q
}

func (d *dex) awaitNoQuestion(t *testing.T, sessionID int64) {
	t.Helper()
	eventually(t, "no question", func() (bool, any) {
		q := d.question(t, sessionID)
		return q == nil, q
	})
}

func (d *dex) awaitIdle(t *testing.T, sessionID int64) {
	t.Helper()
	eventually(t, "session idle", func() (bool, any) {
		var c sessionListItem
		d.must(t, http.MethodGet, fmt.Sprintf("/api/bots/default/sessions/%d", sessionID), nil, &c)
		return !c.Busy, c
	})
}

const askJSON = `ask_user {"question":"How should I handle the banner flicker?","options":["Add it to the running SSE session","Start a separate session","Investigate only"]}`

func (d *dex) answer(t *testing.T, sessionID int64, questionID, text string, want int) string {
	t.Helper()
	id := uuid.NewV7().String()
	d.must(t, http.MethodPost, fmt.Sprintf("/api/bots/default/sessions/%d/messages", sessionID),
		map[string]string{"id": id, "text": text, "answersQuestionId": questionID}, nil, want)
	return id
}

func questionStatus(t *testing.T, conn *sql.DB, id string) string {
	t.Helper()
	var status string
	if err := conn.QueryRowContext(t.Context(), "SELECT status FROM questions WHERE id = ?", id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestUserAnswersQuestionFromCard(t *testing.T) {
	t.Parallel()
	// Given a session whose model asks one question and ends its turn.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "tool")
	conn := taskResultDB(t, dir)
	sessionID := d.newSessionWithMessage(t, askJSON)
	q := d.awaitQuestion(t, sessionID, "open")
	d.awaitIdle(t, sessionID)
	if q.Text != "How should I handle the banner flicker?" || len(q.Options) != 3 {
		t.Fatalf("question: %+v", q)
	}

	// When the user writes a plain message, then the question stays open.
	d.setModel(t, "echo")
	plainID := d.send(t, sessionID, "unrelated")
	d.watch(t, sessionID).until(t, "plain message answered", answered("echo: unrelated"))
	d.must(t, http.MethodPost, sessionPath(sessionID, "messages"),
		map[string]string{"id": plainID, "text": "unrelated", "answersQuestionId": q.ID}, nil, http.StatusConflict)
	if again := d.question(t, sessionID); again == nil || again.ID != q.ID || again.Status != "open" {
		t.Fatalf("plain message changed the question: %+v", again)
	}

	// When the user answers with the question ID, the answer reaches the model and the question closes.
	answerID := d.answer(t, sessionID, q.ID, "Start a separate session", http.StatusAccepted)
	d.watch(t, sessionID).until(t, "answer processed", answeredWith("echo: Answer to: How should I handle the banner flicker?\nStart a separate session"))
	d.awaitNoQuestion(t, sessionID)
	if status := questionStatus(t, conn, q.ID); status != "consumed" {
		t.Fatalf("status: %s", status)
	}

	// Then a second answer to the closed question is refused, and a retry of the first answer is accepted.
	d.answer(t, sessionID, q.ID, "Investigate only", http.StatusConflict)
	d.must(t, http.MethodPost, fmt.Sprintf("/api/bots/default/sessions/%d/messages", sessionID),
		map[string]string{"id": answerID, "text": "Start a separate session", "answersQuestionId": q.ID}, nil, http.StatusAccepted)
	d.must(t, http.MethodPost, sessionPath(sessionID, "messages"),
		map[string]string{"id": answerID, "text": "Start a separate session"}, nil, http.StatusConflict)
}

func TestMixedToolBatchCreatesNoQuestion(t *testing.T) {
	t.Parallel()
	// Given a model that calls ask_user and bash in one message.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "mixed")
	sessionID := d.newSessionWithMessage(t, askJSON)

	// Then ask_user fails, the other tool runs, and no question exists.
	d.watch(t, sessionID).until(t, "tool error for ask_user", answeredWith("ask_user must be the only tool call"))
	if q := d.question(t, sessionID); q != nil {
		t.Fatalf("question: %+v", q)
	}
}

func TestUserStopDismissesQuestion(t *testing.T) {
	t.Parallel()
	// Given a session with an open question.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "tool")
	conn := taskResultDB(t, dir)
	sessionID := d.newSessionWithMessage(t, askJSON)
	q := d.awaitQuestion(t, sessionID, "open")
	d.awaitIdle(t, sessionID)

	// When the user presses Stop, then the question is dismissed and cannot be answered.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/bots/default/sessions/%d/abort", sessionID), nil, nil)
	d.awaitNoQuestion(t, sessionID)
	if status := questionStatus(t, conn, q.ID); status != "dismissed" {
		t.Fatalf("status: %s", status)
	}
	d.answer(t, sessionID, q.ID, "late", http.StatusConflict)
}

func TestUserClearAndArchiveDismissQuestion(t *testing.T) {
	t.Parallel()
	// Given two sessions with open questions.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "tool")
	conn := taskResultDB(t, dir)
	cleared := d.newSessionWithMessage(t, askJSON)
	archived := d.newSessionWithMessage(t, askJSON)
	clearedQ := d.awaitQuestion(t, cleared, "open")
	archivedQ := d.awaitQuestion(t, archived, "open")
	d.awaitIdle(t, cleared)
	d.awaitIdle(t, archived)

	// When the user clears one and archives the other, then both questions are dismissed.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/bots/default/sessions/%d/clear", cleared), map[string]string{"id": uuid.NewV7().String()}, nil, http.StatusAccepted)
	d.archive(t, http.MethodPut, archived, http.StatusOK)
	d.awaitNoQuestion(t, cleared)
	d.awaitNoQuestion(t, archived)
	if a, b := questionStatus(t, conn, clearedQ.ID), questionStatus(t, conn, archivedQ.ID); a != "dismissed" || b != "dismissed" {
		t.Fatalf("statuses: %s %s", a, b)
	}
}

func TestUserWithdrawsAnswerBeforeItRunsAndQuestionReopens(t *testing.T) {
	t.Parallel()
	// Given an open question and a session that is busy with a gated command.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "tool")
	sessionID := d.newSessionWithMessage(t, askJSON)
	q := d.awaitQuestion(t, sessionID, "open")
	d.awaitIdle(t, sessionID)
	d.setModel(t, "bash")
	run := newGatedRun(t, "busy")
	d.send(t, sessionID, run.command)
	run.waitStarted(t)

	// When the user answers, the answer waits in the queue and the question shows it.
	answerID := d.answer(t, sessionID, q.ID, "Investigate only", http.StatusAccepted)
	w := d.watch(t, sessionID)
	w.until(t, "answer queued", queuedItem(answerID))
	if got := d.awaitQuestion(t, sessionID, "answered"); got.Answer != "Investigate only" {
		t.Fatalf("answered question: %+v", got)
	}

	// When the user withdraws the answer before it runs, then the question opens again.
	var back withdrawn
	d.must(t, http.MethodPost, withdrawPath(sessionID, answerID), nil, &back)
	if back.Text != "Investigate only" {
		t.Fatalf("withdrawn answer text: %q", back.Text)
	}
	if got := d.question(t, sessionID); got == nil || got.ID != q.ID || got.Status != "open" {
		t.Fatalf("reopened question: %+v", got)
	}
	// When the user answers again, then the new answer binds to the same question.
	d.answer(t, sessionID, q.ID, "Start a separate session", http.StatusAccepted)
	if got := d.awaitQuestion(t, sessionID, "answered"); got.ID != q.ID || got.Answer != "Start a separate session" {
		t.Fatalf("second answer: %+v", got)
	}
	// And when the gated run ends, then the answer runs and the question is consumed.
	// The queued answer keeps the model of its admission, so bash runs it as a command and fails; the question closes anyway.
	run.open()
	w.until(t, "answer runs", answeredWith("Answer: command not found"))
	d.awaitNoQuestion(t, sessionID)
}

func TestUserAnswersQuestionByQuotingItInTelegram(t *testing.T) {
	t.Parallel()
	// Given a Telegram peer who got a question from Main.
	dir := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, dir, 42)
	d.setModel(t, "tool")
	conn := taskResultDB(t, dir)
	f.message(1, 42, 7, askJSON)
	asked := f.next(t)
	mainID := d.mainSession(t)
	q := d.awaitQuestion(t, mainID, "open")

	// When the peer sends a plain message, then the question stays open.
	d.setModel(t, "echo")
	f.message(2, 42, 7, "unrelated")
	if got := f.next(t).Text; got != "echo: unrelated" {
		t.Fatalf("plain message reply: %q", got)
	}
	if got := d.question(t, mainID); got == nil || got.ID != q.ID || got.Status != "open" {
		t.Fatalf("a plain message changed the question: %+v", got)
	}

	// When the peer quotes a message of an unrelated session position, then it is a plain message too.
	f.reply(3, 42, 7, "stray", asked.ID+1000)
	if got := f.next(t).Text; got != "echo: stray" {
		t.Fatalf("stray quote reply: %q", got)
	}
	if got := d.question(t, mainID); got == nil || got.Status != "open" {
		t.Fatalf("a quote of another message closed the question: %+v", got)
	}

	// When the peer quotes the question message, then the reply answers it.
	f.reply(4, 42, 7, "Start a separate session", asked.ID)
	if got := f.next(t).Text; !strings.Contains(got, "Answer to: How should I handle the banner flicker?") || !strings.Contains(got, "Start a separate session") {
		t.Fatalf("answer reply: %q", got)
	}
	d.awaitNoQuestion(t, mainID)
	if status := questionStatus(t, conn, q.ID); status != "consumed" {
		t.Fatalf("status: %s", status)
	}

	// And a second quote of the same message is a plain message, because the question is closed.
	f.reply(5, 42, 7, "again", asked.ID)
	if got := f.next(t).Text; got != "echo: again" {
		t.Fatalf("quote of a closed question: %q", got)
	}
}

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
		if !strings.HasSuffix(r.URL.Path, "/sendRichMessage") {
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
	var other apiBot
	d.must(t, http.MethodPost, "/api/bots", map[string]string{"name": "Other", "provider": "faux", "modelId": "tool", "thinkingLevel": "off"}, &other, http.StatusCreated)
	bots := []struct {
		bot          *fakeTelegram
		botID, token string
	}{{first, "default", fakeToken}, {second, other.ID, secondToken}}
	conn := sessionDB(t, d)
	for _, entry := range bots {
		base := "/api/bots/" + entry.botID
		d.must(t, http.MethodPut, base+"/channels/telegram", map[string]any{"token": entry.token, "allowedUsers": []int64{ownerID}}, nil)
		entry.bot.message(1, ownerID, ownerID, askJSON)
		if sent := entry.bot.next(t); !strings.Contains(sent.Text, "Reply to this message") {
			t.Fatalf("question reply: %+v", sent)
		}
		eventually(t, "question delivery recorded", func() (bool, any) {
			var delivered bool
			err := conn.QueryRowContext(t.Context(), `SELECT EXISTS (
				SELECT 1 FROM outbox_messages
				WHERE bot_id = ? AND kind = 'question' AND status = 'sent'
			)`, entry.botID).Scan(&delivered)
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
		if !strings.Contains(reply, "echo: Answer to: How should I handle the banner flicker?\nInvestigate only") {
			t.Errorf("bot %s lost its quoted question: %q", entry.botID, reply)
		}
		var main struct {
			Question *questionView `json:"question"`
		}
		d.must(t, http.MethodGet, "/api/bots/"+entry.botID+"/sessions/main", nil, &main)
		if main.Question != nil {
			t.Errorf("bot %s still waits for an answer: %+v", entry.botID, main.Question)
		}
	}
}

func TestUserAnswersTelegramQuestionWithFullQuotedContext(t *testing.T) {
	t.Parallel()
	// Given a pending Question and a real-style quote that contains the bot's full message.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	d.setModel(t, "tool")
	f.message(1, 42, 42, `ask_user {"question":"Which color?","options":["Blue","Green"]}`)
	question := f.next(t)
	d.setModel(t, "echo")
	f.mu.Lock()
	f.updates = append(f.updates, map[string]any{"update_id": int64(2), "message": map[string]any{
		"message_id": 2, "from": map[string]any{"id": 42}, "chat": map[string]any{"id": 42, "type": "private"}, "text": "Blue",
		"reply_to_message": map[string]any{"message_id": question.ID, "from": map[string]any{"id": 123, "first_name": "Bot"}, "text": question.Text},
	}})
	f.mu.Unlock()
	// When the user answers, transport context must not change the stored answer or approval text.
	if got := f.next(t).Text; got != "echo: Answer to: Which color?\nBlue" {
		t.Fatalf("question answer changed: %q", got)
	}
	conn := controlWriteDB(t, d.dataDir(t))
	if n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:2' AND content=?", "Answer to: Which color?\nBlue"); n != 1 {
		t.Fatalf("question answer count=%d", n)
	}
}
