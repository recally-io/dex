package session

import (
	"errors"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserInboxContractRecordsProducerAndRejectsImmutableIntentMismatch(t *testing.T) {
	t.Parallel()
	r := startNative(t, "echo")
	ctx := t.Context()

	// Given a paired Telegram channel on the test bot.
	if _, err := r.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{
		BotID:        "bot",
		Token:        "123:tok",
		AllowedUsers: "42",
		Options:      "{}",
	}); err != nil {
		t.Fatal(err)
	}

	// When the user submits a Web message and a Telegram private message.
	webID := "web:0194f2c0-0000-7000-8000-000000000001"
	if _, err := r.service.Send(ctx, "bot", MainKey, Message{ID: webID, Content: "hello web"}); err != nil {
		t.Fatal(err)
	}
	if err := r.service.AcceptMessage(ctx, "bot", "telegram:42", "telegram:123:1", Message{
		Content:        "hello tg",
		ChannelContext: ChannelContext{AccountID: "123", SenderID: "42"},
	}); err != nil {
		t.Fatal(err)
	}

	// Then Web records producer=user with empty channel reply address, while Telegram records reply_channel and reply_peer.
	webRow, err := r.q.GetSubmission(ctx, webID)
	if err != nil {
		t.Fatal(err)
	}
	if webRow.Producer != ProducerUser || webRow.ReplyChannel != "" || webRow.ReplyPeer != "" {
		t.Fatalf("web submission = (%q, %q, %q), want (user, '', '')", webRow.Producer, webRow.ReplyChannel, webRow.ReplyPeer)
	}
	tgReceipt, err := r.service.LookupSource(ctx, "bot", "telegram:123:1")
	if err != nil || tgReceipt.ID == "" {
		t.Fatalf("lookup telegram source = (%+v, %v)", tgReceipt, err)
	}
	tgRow, err := r.q.GetSubmission(ctx, tgReceipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tgRow.Producer != ProducerUser || tgRow.ReplyChannel != "telegram" || tgRow.ReplyPeer != "42" {
		t.Fatalf("tg submission = (%q, %q, %q), want (user, telegram, 42)", tgRow.Producer, tgRow.ReplyChannel, tgRow.ReplyPeer)
	}

	// When the same request ID is retried with a different reply origin or mode, it is rejected rather than silently deduplicated.
	if _, err := r.service.enqueue(ctx, "bot", MainKey, "", "telegram:42", Message{
		ID:             webID,
		Content:        "hello web",
		ChannelContext: ChannelContext{AccountID: "123", SenderID: "42"},
	}, true); !errors.Is(err, ErrMessageID) {
		t.Fatalf("origin mismatch error = %v, want ErrMessageID", err)
	}
	if _, err := r.service.Send(ctx, "bot", MainKey, Message{
		ID:       webID,
		Content:  "hello web",
		WhenBusy: "steer",
	}); !errors.Is(err, ErrMessageID) {
		t.Fatalf("mode mismatch error = %v, want ErrMessageID", err)
	}
}
