package chat

import (
	"database/sql"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
)

func TestUserReceivesDistinctNotificationsFromTheSameRun(t *testing.T) {
	r := startNative(t, "echo")
	ctx := t.Context()
	_, call := taskParent(t, r)
	// Given a default Telegram target and a paired WeChat target.
	if _, err := r.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{AgentID: "bot", Token: "123:fixture", AllowedUsers: "42"}); err != nil {
		t.Fatal(err)
	}
	if err := r.q.SetWechatSettings(ctx, sqlc.SetWechatSettingsParams{AgentID: "bot", BotID: "wx", Owner: "owner"}); err != nil {
		t.Fatal(err)
	}
	if err := r.q.InsertRoutineNotificationTarget(ctx, sqlc.InsertRoutineNotificationTargetParams{
		ID: "tg", AgentID: "bot", Platform: "telegram", AccountID: "123", Peer: "telegram:42",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.SetPrimaryNotificationTarget(ctx, sqlc.SetPrimaryNotificationTargetParams{
		ID: "bot", PrimaryNotificationTargetID: sql.NullString{String: "tg", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	// When two tool calls in the same run select different targets.
	for _, tc := range []struct{ key, channel, peer string }{
		{"bot/101", "", "telegram:42"},
		{"bot/102", "wechat", "wechat:owner"},
	} {
		call.Key = tc.key
		args := NotifyUserArgs{Content: tc.peer, Channel: tc.channel, Final: new(false)}
		id, err := r.service.ToolNotifyUser(ctx, call, args)
		if err != nil {
			t.Fatal(err)
		}
		// Then each is stored before the source run settles. Replays keep the receipt.
		row, err := r.q.GetOutboxMessage(ctx, id)
		if err != nil || row.Peer != tc.peer || row.Body != tc.peer || row.Status != "pending" {
			t.Fatalf("notification = %+v, err = %v", row, err)
		}
		if again, err := r.service.ToolNotifyUser(ctx, call, args); err != nil || again != id {
			t.Fatalf("replay = %q, want %q: %v", again, id, err)
		}
	}
	if source, err := r.q.GetSubmission(ctx, call.SourceRequestID); err != nil || source.Status != "pending" {
		t.Fatalf("tool waited for settlement: status=%q, err=%v", source.Status, err)
	}
}
