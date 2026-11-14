package app_test

import (
	"strings"
	"testing"
)

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

func TestUserLegacyPartialTelegramReplyKeepsOriginalRendering(t *testing.T) {
	t.Parallel()
	// Given a confirmed first text part, as an older version stored before frozen reply payloads.
	f := newFakeTelegram(t)
	f.failAfter.Store(1)
	data := t.TempDir()
	d := startTelegram(t, f, data, 42)
	f.message(1, 42, 42, strings.Repeat("a", 5000)+"\n\n~~removed~~")
	_ = f.next(t)
	conn := controlWriteDB(t, data)
	eventually(t, "confirmed first part and pending remainder", func() (bool, any) {
		n := count(t, conn, "SELECT count(*) FROM submissions WHERE source_key='telegram:123:1' AND delivered_parts=1 AND delivered_at IS NULL")
		return n == 1, n
	})
	d.stop()
	execSQL(t, conn, "UPDATE submissions SET rendered_reply='' WHERE source_key='telegram:123:1'")
	// When the current renderer resumes, the legacy cursor still uses the original Markdown rules.
	f.failAfter.Store(0)
	startDex(t, data, f.srv.URL)
	got := f.next(t).Text
	if !strings.Contains(got, "~~removed~~") || strings.Contains(got, "<s>") {
		t.Fatalf("legacy reply used a new renderer: %q", got)
	}
}
