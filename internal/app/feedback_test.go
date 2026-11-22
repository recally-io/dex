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
			// Then the paired chat gets instructions and a literal draft, not a model submission.
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
				err := sessionDB(t, e.d).QueryRowContext(t.Context(), `SELECT count(*), coalesce(sum(delivered_at IS NULL), 0), coalesce(sum(answer != '' OR rendered_reply != ''), 0) FROM channel_commands WHERE command = '/feedback'`).Scan(&total, &pending, &retained)
				return err == nil && total == 2 && pending == 0 && retained == 0, fmt.Sprintf("total=%d pending=%d retained=%d err=%v", total, pending, retained, err)
			})
		})
	}
}

func TestUserTelegramGroupFeedbackOnlyPromptsEligibleSenderAndIgnoresReplay(t *testing.T) {
	t.Parallel()
	// Given Telegram's native command menu and a topic with an eligible sender.
	f := newFakeTelegram(t)
	d := startTelegram(t, f, t.TempDir(), 42)
	menu := f.nextEvent(t, "setMyCommands")
	// When a stranger and then the paired sender try feedback; Telegram repeats the latter update.
	f.groupText(1, 43, -100, 7, "/feedback NOT_PAIRED")
	f.groupText(2, 42, -100, 7, "/feedback@dex_test_bot GROUP_SECRET\nGROUP_DESCRIPTION")
	// Then the only send is a private-chat redirect in that topic, with no added text or controls.
	if !strings.Contains(menu.Text, "/feedback ") {
		t.Fatalf("feedback missing from native command list: %s", menu.Text)
	}
	got := f.next(t)
	if got.ChatID != -100 || got.TopicID != 7 || !strings.Contains(got.Text, "private chat") || strings.Contains(got.Text, "GROUP_") || strings.Contains(got.Text, feedbackURL) {
		t.Fatalf("unexpected group reply: %+v", got)
	}
	f.groupText(3, 42, -100, 7, "/feedback@another_bot IGNORE_ME")
	f.groupText(4, 42, -100, 7, "/feedback\nANOTHER_TITLE\nANOTHER_DESCRIPTION")
	f.groupText(4, 42, -100, 7, "/feedback\nANOTHER_TITLE\nANOTHER_DESCRIPTION")
	f.groupText(5, 42, -100, 7, "/feedback")
	for range 2 {
		if got := f.next(t); !strings.Contains(got.Text, "private chat") || got.TopicID != 7 {
			t.Fatalf("unexpected redirect: %+v", got)
		}
	}
	eventually(t, "Telegram acknowledged all updates", func() (bool, any) { offset := f.pollOffset.Load(); return offset >= 6, offset })
	if n := f.sendCount.Load(); n != 3 {
		t.Fatalf("group sends = %d, want 3", n)
	}
	assertNoStoredInput(t, d)
	var receipts int
	if err := sessionDB(t, d).QueryRowContext(t.Context(), `SELECT count(*) FROM channel_commands WHERE command = '/feedback'`).Scan(&receipts); err != nil || receipts != 3 {
		t.Fatalf("group receipts=%d, err=%v", receipts, err)
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
		err := sessionDB(t, d).QueryRowContext(t.Context(), `SELECT count(*), coalesce(sum(delivered_at IS NOT NULL AND answer = '' AND rendered_reply = ''), 0) FROM channel_commands WHERE command = '/feedback'`).Scan(&total, &clean)
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
