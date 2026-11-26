package app_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const feedbackURL = "https://github.com/recally-io/dex/issues/new"

func TestUserPreviewsFeedbackInPairedPrivateChannelsWithoutModel(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"telegram", "feishu", "wechat"} {
		t.Run(platform, func(t *testing.T) {
			// Given a paired private channel and no configured model.
			e := newMainEntry(t, platform)
			var a apiAgent
			e.d.must(t, http.MethodGet, "/api/agents/default", nil, &a)
			a.Provider, a.ModelID = "", ""
			e.d.must(t, http.MethodPut, "/api/agents/default", a, nil)
			// When the user asks for feedback and supplies a title and description.
			e.send("/feedback")
			e.send("/feedback A *literal* title\nDescription <literal>\nSecond line")
			// Then the paired session gets instructions and a literal draft, not a model submission.
			eventually(t, "feedback instructions and draft", func() (bool, any) {
				var prompted, previewed bool
				for _, send := range e.sends() {
					if send.Peer != e.peer {
						continue
					}
					prompted = prompted || strings.Contains(send.Text, "first line")
					previewed = previewed || strings.Contains(send.Text, "A *literal* title") && strings.Contains(send.Text, "Description <literal>\nSecond line") && strings.Contains(send.Text, "public") && strings.Contains(send.Text, feedbackURL) && !strings.Contains(send.Text, feedbackURL+"?")
				}
				return prompted && previewed, e.sends()
			})
			assertNoStoredInput(t, e.d)
			eventually(t, "delivered feedback receipts have no draft text", func() (bool, any) {
				var total, pending, retained int
				err := sessionDB(t, e.d).QueryRowContext(t.Context(), `SELECT count(*), coalesce(sum(c.delivered_at IS NULL), 0),
					coalesce(sum(c.answer != '' OR coalesce(o.rendered_message, '') != ''), 0)
					FROM channel_commands c LEFT JOIN outbox_messages o ON o.id = c.source_key WHERE c.command = '/feedback'`).Scan(&total, &pending, &retained)
				return err == nil && total == 2 && pending == 0 && retained == 0, fmt.Sprintf("total=%d pending=%d retained=%d err=%v", total, pending, retained, err)
			})
		})
	}
}

func TestUserTelegramPrivateFeedbackRetryDoesNotCreateASecondDraft(t *testing.T) {
	t.Parallel()
	// Given a paired Telegram chat and a temporarily unavailable reply transport.
	f := newFakeTelegram(t)
	f.failSend.Store(true)
	dir := t.TempDir()
	d := startTelegram(t, f, dir, 42)
	// When Telegram sends a private draft request with a quote and repeats the same source update.
	update := map[string]any{"update_id": int64(1), "message": map[string]any{
		"message_id": 1, "from": map[string]any{"id": 42}, "chat": map[string]any{"id": 7, "type": "private"},
		"text": "/feedback A private title\nA private description", "quote": map[string]any{"text": "UNRELATED_QUOTE"},
	}}
	f.mu.Lock()
	f.updates = append(f.updates, update, update)
	f.mu.Unlock()
	eventually(t, "Telegram acknowledges the repeated input even while delivery waits", func() (bool, any) {
		offset := f.pollOffset.Load()
		return offset >= 2, offset
	})
	d.stop()
	f.failSend.Store(false)
	d = startDex(t, dir, f.srv.URL)
	// Then the pending reply succeeds after a restart without including the quote, and leaves only the receipt.
	got := f.next(t)
	if got.ChatID != 7 || !strings.Contains(got.RichHTML, `<a href="`+feedbackURL+`">`) || !strings.Contains(got.RichHTML, "<pre><code>") || !strings.Contains(got.Text, "A private title") || !strings.Contains(got.Text, "A private description") || strings.Contains(got.Text, "UNRELATED_QUOTE") {
		t.Fatalf("unexpected private reply: %+v", got)
	}
	eventually(t, "one delivered feedback receipt with no reply content", func() (bool, any) {
		var total, clean int
		err := sessionDB(t, d).QueryRowContext(t.Context(), `SELECT count(*), coalesce(sum(c.delivered_at IS NOT NULL AND c.answer = '' AND o.rendered_message = ''), 0)
			FROM channel_commands c JOIN outbox_messages o ON o.id = c.source_key WHERE c.command = '/feedback'`).Scan(&total, &clean)
		return err == nil && total == 1 && clean == 1, fmt.Sprintf("total=%d clean=%d err=%v", total, clean, err)
	})
	assertNoStoredInput(t, d)
}

func TestUserChannelFeedbackWithFenceGetsAnInstructionInsteadOfAnUnsafePreview(t *testing.T) {
	t.Parallel()
	// Given a paired private chat.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	// When the user enters a draft with an embedded Markdown fence.
	f.message(1, 42, 7, "/feedback PRIVATE_TITLE\nDescription\n```\nPRIVATE_CODE\n```")
	// Then the response explains the limit without echoing the draft or admitting it to the model.
	got := f.next(t)
	if !strings.Contains(got.Text, "code fences") || !strings.Contains(got.Text, "Web") || strings.Contains(got.Text, "PRIVATE_") {
		t.Fatalf("unexpected feedback reply: %+v", got)
	}
	assertNoStoredInput(t, d)
}
