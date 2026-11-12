package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
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

func (d *dex) question(t *testing.T, chatID int64) *questionView {
	t.Helper()
	var c struct {
		Question *questionView `json:"question"`
	}
	d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", chatID), nil, &c)
	return c.Question
}

// awaitQuestion polls the chat until it shows a question with the status.
func (d *dex) awaitQuestion(t *testing.T, chatID int64, status string) *questionView {
	t.Helper()
	var q *questionView
	eventually(t, "question "+status, func() (bool, any) {
		q = d.question(t, chatID)
		return q != nil && q.Status == status, q
	})
	return q
}

func (d *dex) awaitNoQuestion(t *testing.T, chatID int64) {
	t.Helper()
	eventually(t, "no question", func() (bool, any) {
		q := d.question(t, chatID)
		return q == nil, q
	})
}

func (d *dex) awaitIdle(t *testing.T, chatID int64) {
	t.Helper()
	eventually(t, "chat idle", func() (bool, any) {
		var c chatInfo
		d.must(t, http.MethodGet, fmt.Sprintf("/api/agents/default/chats/%d", chatID), nil, &c)
		return !c.Busy, c
	})
}

const askJSON = `ask_user {"question":"How should I handle the banner flicker?","options":["Add it to the running SSE session","Start a separate session","Investigate only"]}`

func (d *dex) answer(t *testing.T, chatID int64, questionID, text string, want int) string {
	t.Helper()
	id := uuid.NewV7().String()
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/messages", chatID),
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
	// Given a chat whose model asks one question and ends its turn.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "tool")
	conn := taskResultDB(t, dir)
	chatID := d.newChat(t, askJSON)
	q := d.awaitQuestion(t, chatID, "open")
	d.awaitIdle(t, chatID)
	if q.Text != "How should I handle the banner flicker?" || len(q.Options) != 3 {
		t.Fatalf("question: %+v", q)
	}

	// When the user writes a plain message, then the question stays open.
	d.setModel(t, "echo")
	plainID := d.send(t, chatID, "unrelated")
	d.watch(t, chatID).until(t, "plain message answered", answered("echo: unrelated"))
	d.must(t, http.MethodPost, chatPath(chatID, "messages"),
		map[string]string{"id": plainID, "text": "unrelated", "answersQuestionId": q.ID}, nil, http.StatusConflict)
	if again := d.question(t, chatID); again == nil || again.ID != q.ID || again.Status != "open" {
		t.Fatalf("plain message changed the question: %+v", again)
	}

	// When the user answers with the question ID, the answer reaches the model and the question closes.
	answerID := d.answer(t, chatID, q.ID, "Start a separate session", http.StatusAccepted)
	d.watch(t, chatID).until(t, "answer processed", answeredWith("echo: Answer to: How should I handle the banner flicker?\nStart a separate session"))
	d.awaitNoQuestion(t, chatID)
	if status := questionStatus(t, conn, q.ID); status != "consumed" {
		t.Fatalf("status: %s", status)
	}

	// Then a second answer to the closed question is refused, and a retry of the first answer is accepted.
	d.answer(t, chatID, q.ID, "Investigate only", http.StatusConflict)
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/messages", chatID),
		map[string]string{"id": answerID, "text": "Start a separate session", "answersQuestionId": q.ID}, nil, http.StatusAccepted)
	d.must(t, http.MethodPost, chatPath(chatID, "messages"),
		map[string]string{"id": answerID, "text": "Start a separate session"}, nil, http.StatusConflict)
}

func TestUserCannotOpenSecondQuestion(t *testing.T) {
	t.Parallel()
	// Given a chat with an open question.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "tool")
	chatID := d.newChat(t, askJSON)
	q := d.awaitQuestion(t, chatID, "open")
	d.awaitIdle(t, chatID)

	// When the model asks again, then the tool fails and the first question stays.
	d.send(t, chatID, strings.Replace(askJSON, "banner flicker", "other topic", 1))
	d.watch(t, chatID).until(t, "tool error", answeredWith("a question is already open"))
	if again := d.question(t, chatID); again == nil || again.ID != q.ID || !strings.Contains(again.Text, "banner") {
		t.Fatalf("question was replaced: %+v", again)
	}
}

func TestMixedToolBatchCreatesNoQuestion(t *testing.T) {
	t.Parallel()
	// Given a model that calls ask_user and bash in one message.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "mixed")
	chatID := d.newChat(t, askJSON)

	// Then ask_user fails, the other tool runs, and no question exists.
	d.watch(t, chatID).until(t, "tool error for ask_user", answeredWith("ask_user must be the only tool call"))
	if q := d.question(t, chatID); q != nil {
		t.Fatalf("question: %+v", q)
	}
}

func TestUserStopDismissesQuestion(t *testing.T) {
	t.Parallel()
	// Given a chat with an open question.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "tool")
	conn := taskResultDB(t, dir)
	chatID := d.newChat(t, askJSON)
	q := d.awaitQuestion(t, chatID, "open")
	d.awaitIdle(t, chatID)

	// When the user presses Stop, then the question is dismissed and cannot be answered.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/abort", chatID), nil, nil)
	d.awaitNoQuestion(t, chatID)
	if status := questionStatus(t, conn, q.ID); status != "dismissed" {
		t.Fatalf("status: %s", status)
	}
	d.answer(t, chatID, q.ID, "late", http.StatusConflict)
}

func TestUserClearAndArchiveDismissQuestion(t *testing.T) {
	t.Parallel()
	// Given two chats with open questions.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "tool")
	conn := taskResultDB(t, dir)
	cleared := d.newChat(t, askJSON)
	archived := d.newChat(t, askJSON)
	clearedQ := d.awaitQuestion(t, cleared, "open")
	archivedQ := d.awaitQuestion(t, archived, "open")
	d.awaitIdle(t, cleared)
	d.awaitIdle(t, archived)

	// When the user clears one and archives the other, then both questions are dismissed.
	d.must(t, http.MethodPost, fmt.Sprintf("/api/agents/default/chats/%d/clear", cleared), map[string]string{"id": uuid.NewV7().String()}, nil, http.StatusAccepted)
	d.archive(t, http.MethodPut, archived, http.StatusOK)
	d.awaitNoQuestion(t, cleared)
	d.awaitNoQuestion(t, archived)
	if a, b := questionStatus(t, conn, clearedQ.ID), questionStatus(t, conn, archivedQ.ID); a != "dismissed" || b != "dismissed" {
		t.Fatalf("statuses: %s %s", a, b)
	}
}

func TestUserWithdrawsAnswerBeforeItRunsAndQuestionReopens(t *testing.T) {
	t.Parallel()
	// Given an open question and a chat that is busy with a gated command.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "tool")
	chatID := d.newChat(t, askJSON)
	q := d.awaitQuestion(t, chatID, "open")
	d.awaitIdle(t, chatID)
	d.setModel(t, "bash")
	run := newGatedRun(t, "busy")
	d.send(t, chatID, run.command)
	run.waitStarted(t)

	// When the user answers, the answer waits in the queue and the question shows it.
	answerID := d.answer(t, chatID, q.ID, "Investigate only", http.StatusAccepted)
	w := d.watch(t, chatID)
	w.until(t, "answer queued", queuedItem(answerID))
	if got := d.awaitQuestion(t, chatID, "answered"); got.Answer != "Investigate only" {
		t.Fatalf("answered question: %+v", got)
	}

	// When the user withdraws the answer before it runs, then the question opens again.
	var back withdrawn
	d.must(t, http.MethodPost, withdrawPath(chatID, answerID), nil, &back)
	if back.Text != "Investigate only" {
		t.Fatalf("withdrawn answer text: %q", back.Text)
	}
	if got := d.question(t, chatID); got == nil || got.ID != q.ID || got.Status != "open" {
		t.Fatalf("reopened question: %+v", got)
	}
	// When the user answers again, then the new answer binds to the same question.
	d.answer(t, chatID, q.ID, "Start a separate session", http.StatusAccepted)
	if got := d.awaitQuestion(t, chatID, "answered"); got.ID != q.ID || got.Answer != "Start a separate session" {
		t.Fatalf("second answer: %+v", got)
	}
	// And when the gated run ends, then the answer runs and the question is consumed.
	// The queued answer keeps the model of its admission, so bash runs it as a command and fails; the question closes anyway.
	run.open()
	w.until(t, "answer runs", answeredWith("Answer: command not found"))
	d.awaitNoQuestion(t, chatID)
}

func TestUserSeesQuestionInTelegram(t *testing.T) {
	t.Parallel()
	// Given a Telegram peer who talks to Main.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	d.setModel(t, "tool")

	// When the model asks a question, then the peer receives the question with lettered options.
	f.message(1, 42, 7, askJSON)
	text := f.next(t).Text
	for _, want := range []string{"How should I handle the banner flicker?", "A. Add it to the running SSE session", "C. Investigate only", "Reply to this message to answer", "/a/default"} {
		if !strings.Contains(text, want) {
			t.Fatalf("telegram text lacks %q: %q", want, text)
		}
	}
	// And the question waits in Main.
	d.awaitQuestion(t, d.mainChat(t), "open")
}

func TestUserQuestionToolIsUnavailableInAutomationAndBackground(t *testing.T) {
	t.Parallel()
	// Given a model that asks a question in an automation and in a background task.
	data := t.TempDir()
	d := startDex(t, data, "")
	d.setModel(t, "tool")
	a := d.createAutomation(t, "No questions", askJSON, "new", map[string]any{"kind": "daily", "time": "09:00"})
	run := d.runNow(t, a.ID)

	// Then the tool does not exist there, and no question is saved.
	d.watch(t, run.ChatID).until(t, "ask_user unavailable in automation", answeredWith("Tool ask_user is not available"))
	parent := d.newSession(t).ID
	spawnExecution(t, d, parent, "No questions", askJSON, "tool")
	task := waitExecution(t, taskResultDB(t, data), parent, "No questions", "done")
	if !strings.Contains(task.Report, "Tool ask_user is not available") {
		t.Fatalf("background report: %s", task.Report)
	}
	if q := d.question(t, run.ChatID); q != nil {
		t.Fatalf("automation question: %+v", q)
	}
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
	mainID := d.mainChat(t)
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

	// When the peer quotes a message of an unrelated chat position, then it is a plain message too.
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
