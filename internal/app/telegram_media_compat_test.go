package app_test

import "testing"

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
