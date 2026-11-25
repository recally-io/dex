package chat

import (
	"database/sql"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func TestUserChannelRevocationRetiresOldChildResultsWithoutOptIn(t *testing.T) {
	for _, platform := range []string{"telegram", "feishu", "wechat"} {
		for _, phase := range []string{"before result", "already queued"} {
			t.Run(platform+"/"+phase, func(t *testing.T) {
				r := startNative(t, "echo")
				ctx := t.Context()
				parent, call := taskParent(t, r)
				change := func(owner string) {
					t.Helper()
					var err error
					switch platform {
					case "telegram":
						_, err = r.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{AgentID: "bot", Token: "123:fixture", AllowedUsers: owner, Options: "{}"})
					case "feishu":
						err = r.q.SetFeishuSettings(ctx, sqlc.SetFeishuSettingsParams{AgentID: "bot", AppID: "123", OwnerID: owner, Brand: "feishu"})
					case "wechat":
						err = r.q.SetWechatSettings(ctx, sqlc.SetWechatSettingsParams{AgentID: "bot", BotID: "123", Owner: owner})
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				change("42")
				peer := platform + ":42"
				binding := func() sql.NullInt64 {
					t.Helper()
					id, err := ChannelInputBinding(ctx, r.q, "bot", peer, ChannelContext{AccountID: "123", SenderID: "42"})
					if err != nil {
						t.Fatal(err)
					}
					return id
				}
				oldBinding := binding()
				// Given the original acknowledgment was sent while the child still had work to do.
				call.SourceRequestID = "channel-request"
				if _, err := r.q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{ID: call.SourceRequestID, BindingID: oldBinding, ConversationID: parent, Content: "Please report", Attachments: "null", Origin: platform + ":42", Kind: "message", SourceKey: sql.NullString{String: platform + ":123:1", Valid: true}, ChannelContext: `{"accountId":"123","senderId":"42"}`}); err != nil {
					t.Fatal(err)
				}
				if _, err := r.service.ToolSessionNew(ctx, call, SessionNewArgs{Title: "Worker", Message: "child findings", FollowUp: false}); err != nil {
					t.Fatal(err)
				}
				if err := r.service.settle(ctx, call.SourceRequestID, engine.Outcome{Status: "done", Answer: "I will report back"}); err != nil {
					t.Fatal(err)
				}
				if err := r.q.MarkChannelReplyDelivered(ctx, sqlc.MarkChannelReplyDeliveredParams{ID: call.SourceRequestID, DeliveredAt: sql.NullInt64{Int64: 42, Valid: true}}); err != nil {
					t.Fatal(err)
				}
				turn := turnID(call.Key)
				resultID := "result:" + turn
				settleChild := func() {
					t.Helper()
					if err := r.service.admit(t.Context(), taskInput(t, r, turn)); err != nil {
						t.Fatal(err)
					}
					awaitReceipt(t, r, turn)
				}
				notify := func() sqlc.OutboxMessage {
					t.Helper()
					action := call
					action.SourceRequestID = resultID
					action.Key += "/notify"
					id, err := r.service.ToolNotifyUser(ctx, action, NotifyUserArgs{Content: "Reviewed conclusion"})
					if err != nil {
						t.Fatal(err)
					}
					row, err := r.q.GetOutboxMessage(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					return row
				}
				var queued sqlc.OutboxMessage
				if phase == "already queued" {
					settleChild()
					queued = notify()
					if queued.Status != "pending" {
						t.Fatalf("notification was not queued: %+v", queued)
					}
				}
				// Revocation is sticky across re-pairing, for existing and future results.
				change("99")
				change("42")
				if phase == "before result" {
					settleChild()
					queued = notify()
				}
				row, err := r.q.GetOutboxMessage(ctx, queued.ID)
				if err != nil || row.Status != "revoked" || row.DeliveredAt.Valid {
					t.Fatalf("old notification was revived: %+v, %v", row, err)
				}
				result, err := r.q.GetSubmission(ctx, resultID)
				if err != nil || result.BindingID != oldBinding || row.BindingID != oldBinding {
					t.Fatalf("old result or notification lost its binding: result=%+v outbox=%+v error=%v", result, row, err)
				}
				newBinding := binding()
				if newBinding == oldBinding {
					t.Fatal("re-pairing reused the old binding")
				}
				// The restored grant still admits new channel work.
				fresh := "channel-request-after-pair"
				if _, err := r.q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{ID: fresh, BindingID: newBinding, ConversationID: parent, Content: "New request", Attachments: "null", Origin: platform + ":42", Kind: "message", SourceKey: sql.NullString{String: platform + ":123:2", Valid: true}}); err != nil {
					t.Fatal(err)
				}
				if err := r.service.settle(ctx, fresh, engine.Outcome{Status: "done", Answer: "New response"}); err != nil {
					t.Fatal(err)
				}
				reply, err := r.q.GetOutboxMessage(ctx, fresh)
				if err != nil || reply.Status != "pending" {
					t.Fatalf("new request did not get a reply: %+v, %v", reply, err)
				}

			})
		}
	}
}
