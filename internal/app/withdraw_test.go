package app_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"uuid"
)

// gatedRun is a real bash command that waits for a release file.
type gatedRun struct {
	command, started, release string
}

func newGatedRun(t *testing.T, name string) gatedRun {
	t.Helper()
	dir := t.TempDir()
	g := gatedRun{started: filepath.Join(dir, name+".started"), release: filepath.Join(dir, name+".release")}
	g.command = fmt.Sprintf("touch '%s'; while [ ! -f '%s' ]; do sleep 0.02; done; echo finished", g.started, g.release)
	t.Cleanup(g.open)
	return g
}

func (g gatedRun) open() { _ = os.WriteFile(g.release, nil, 0o600) }

func (g gatedRun) waitStarted(t *testing.T) {
	t.Helper()
	eventually(t, "gated command running", func() (bool, any) { _, err := os.Stat(g.started); return err == nil, err })
}

type withdrawn struct {
	ID          string       `json:"id"`
	Text        string       `json:"text"`
	Attachments []attachment `json:"attachments"`
}

func withdrawPath(chatID int64, id string) string {
	return fmt.Sprintf("/api/agents/default/chats/%d/messages/%s/withdraw", chatID, id)
}

func queuedItem(id string) func(*transcript) bool {
	return func(tr *transcript) bool {
		for _, it := range tr.items {
			if it.ID == id && it.Queued {
				return true
			}
		}
		return false
	}
}

func receiptState(t *testing.T, conn *sql.DB, id string) (status, reason string, delivered bool) {
	t.Helper()
	var reasonValue sql.NullString
	var deliveredAt sql.NullInt64
	if err := conn.QueryRowContext(t.Context(), "SELECT status, reason, delivered_at FROM submissions WHERE id = ?", id).Scan(&status, &reasonValue, &deliveredAt); err != nil {
		t.Fatal(err)
	}
	return status, reasonValue.String, deliveredAt.Valid
}

func TestUserWithdrawsQueuedMessageAndItNeverRuns(t *testing.T) {
	t.Parallel()
	// Given a chat that runs a gated command and a second message that waits in the native queue.
	dir := t.TempDir()
	d := startDex(t, dir, "")
	d.setModel(t, "bash")
	conn := taskResultDB(t, dir)
	chatID := d.newSession(t).ID
	first := newGatedRun(t, "first")
	d.send(t, chatID, first.command)
	first.waitStarted(t)
	ran := filepath.Join(t.TempDir(), "second-ran")
	secondText := fmt.Sprintf("touch '%s'; echo second", ran)
	secondID := d.send(t, chatID, secondText)
	w := d.watch(t, chatID)
	w.until(t, "second message queued", queuedItem(secondID))

	// When the user withdraws the queued message twice.
	var once, twice withdrawn
	d.must(t, http.MethodPost, withdrawPath(chatID, secondID), nil, &once)
	d.must(t, http.MethodPost, withdrawPath(chatID, secondID), nil, &twice)

	// Then both calls return the text, and the queue no longer shows the message.
	if once.ID != secondID || !strings.Contains(once.Text, "touch") || once.Text != twice.Text || twice.ID != secondID {
		t.Fatalf("withdrawn results differ: %+v %+v", once, twice)
	}
	w.until(t, "withdrawn message leaves the queue", func(tr *transcript) bool {
		for _, it := range tr.items {
			if it.ID == secondID {
				return false
			}
		}
		return true
	})

	// When the first run ends, then the withdrawn message never runs, and its receipt stays.
	first.open()
	w.until(t, "first answer", answered("bash: finished"))
	d.send(t, chatID, "echo after")
	w.until(t, "later message runs", answered("bash: after"))
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("the withdrawn message ran")
	}
	if status, reason, _ := receiptState(t, conn, secondID); status != "unanswered" || reason != "withdrawn" {
		t.Fatalf("receipt: %s %s", status, reason)
	}

	// When the client retries the same request, then it does not run either. The same ID with
	// another text is not a retry.
	messages := fmt.Sprintf("/api/agents/default/chats/%d/messages", chatID)
	d.must(t, http.MethodPost, messages, map[string]string{"id": secondID, "text": secondText}, nil, http.StatusAccepted)
	d.must(t, http.MethodPost, messages, map[string]string{"id": secondID, "text": "echo retried"}, nil, http.StatusConflict)
	d.send(t, chatID, "echo last")
	w.until(t, "last message runs", answered("bash: last"))
	if status, reason, _ := receiptState(t, conn, secondID); status != "unanswered" || reason != "withdrawn" {
		t.Fatalf("receipt after retry: %s %s", status, reason)
	}
}

func TestUserCannotWithdrawMessageThatStarted(t *testing.T) {
	t.Parallel()
	// Given a message whose run has started.
	d := startDex(t, t.TempDir(), "")
	d.setModel(t, "bash")
	chatID := d.newSession(t).ID
	run := newGatedRun(t, "run")
	id := d.send(t, chatID, run.command)
	run.waitStarted(t)

	// When the user withdraws it, then dex answers 409 and the run continues.
	d.must(t, http.MethodPost, withdrawPath(chatID, id), nil, nil, http.StatusConflict)
	run.open()
	d.watch(t, chatID).until(t, "run finishes", answered("bash: finished"))
	// A finished message cannot be withdrawn either.
	d.must(t, http.MethodPost, withdrawPath(chatID, id), nil, nil, http.StatusConflict)
	d.must(t, http.MethodPost, withdrawPath(chatID, uuid.NewV7().String()), nil, nil, http.StatusNotFound)
}

func TestUserWithdrawnChannelMessageIsNotRunByRedelivery(t *testing.T) {
	t.Parallel()
	// Given a Telegram message that waits behind a gated command in Main.
	dir := t.TempDir()
	f := newFakeTelegram(t)
	d := startTelegram(t, f, dir, 42)
	d.setModel(t, "bash")
	run := newGatedRun(t, "run")
	f.message(1, 42, 7, run.command)
	run.waitStarted(t)
	ran := filepath.Join(t.TempDir(), "second-ran")
	f.message(2, 42, 7, fmt.Sprintf("touch '%s'; echo second", ran))
	mainID := d.mainChat(t)
	w := d.watch(t, mainID)
	var queued string
	w.until(t, "channel message queued", func(tr *transcript) bool {
		for _, it := range tr.items {
			if it.Queued && strings.Contains(it.Text, "second") {
				queued = it.ID
			}
		}
		return queued != ""
	})

	// When the user withdraws it on the web and Telegram delivers the same update after a restart.
	d.must(t, http.MethodPost, withdrawPath(mainID, queued), nil, nil)
	d.stop()
	f2 := newFakeTelegram(t)
	d2 := startTelegram(t, f2, dir, 42)
	d2.setModel(t, "bash")
	f2.message(2, 42, 7, fmt.Sprintf("touch '%s'; echo second", ran))
	f2.message(3, 42, 7, "echo after")

	// Then the message does not run, and the peer gets no reply for it.
	for {
		text := f2.next(t).Text
		if strings.Contains(text, "second") || strings.Contains(text, "withdrawn") {
			t.Fatalf("the peer received %q for the withdrawn message", text)
		}
		if text == "bash: after" {
			break
		}
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("the withdrawn message ran after redelivery")
	}
	if status, reason, delivered := receiptState(t, taskResultDB(t, dir), queued); status != "unanswered" || reason != "withdrawn" || !delivered {
		t.Fatalf("receipt: %s %s delivered=%t", status, reason, delivered)
	}
}
