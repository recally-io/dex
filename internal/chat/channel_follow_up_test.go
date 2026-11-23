package chat

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func followUpResult(t *testing.T, r *nativeTest) (int64, engine.TaskContext) {
	t.Helper()
	ctx := t.Context()
	parent, call := taskParent(t, r)
	call.SourceRequestID = "channel-request"
	if _, err := r.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{AgentID: "bot", Token: "123:fixture", AllowedUsers: "42", Options: "{}"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{ID: call.SourceRequestID, ConversationID: parent, Content: "Please report back", Attachments: "null", SourceKey: sql.NullString{String: "telegram:123:1", Valid: true}, Origin: "telegram:42", Kind: "message", ChannelContext: `{"accountId":"123","senderId":"42"}`}); err != nil {
		t.Fatal(err)
	}
	child, err := r.service.ToolSessionNew(ctx, call, engine.SessionNewArgs{Title: "Existing child", Message: "work"})
	if err != nil {
		t.Fatal(err)
	}
	call.Key = "bot/send-follow-up"
	if _, err := r.service.ToolSessionSend(ctx, call, engine.SessionSendArgs{ID: child.ID, Message: "private child data", FollowUp: true}); err != nil {
		t.Fatal(err)
	}
	turn := turnID(call.Key)
	if err := r.service.admit(ctx, taskInput(t, r, turn)); err != nil {
		t.Fatal(err)
	}
	awaitReceipt(t, r, turn)
	call.SourceRequestID = "result:" + turn
	call.RunInputIDs = []string{call.SourceRequestID}
	call.Key = "bot/result-action"
	sub, err := r.q.GetSubmission(ctx, call.SourceRequestID)
	if err != nil || !strings.Contains(sub.Content, "Plain final text is internal") {
		t.Fatalf("original result has no follow-up guidance: %+v, %v", sub, err)
	}
	for _, id := range []string{"source", "channel-request"} {
		if _, err := r.q.SettleSubmission(ctx, sqlc.SettleSubmissionParams{ID: id, Status: "done"}); err != nil {
			t.Fatal(err)
		}
	}
	return parent, call
}

func TestUserChannelFollowUpQueuesOneExplicitConclusion(t *testing.T) {
	r := startNative(t, "echo")
	ctx := t.Context()
	// Given an original result bound to a direct channel request.
	_, call := followUpResult(t, r)
	// When the parent sends progress, and then sends a reviewed conclusion twice.
	progressID, err := r.service.ToolNotifyUser(ctx, call, engine.NotifyUserArgs{Content: "Working on it", Final: new(false)})
	if err != nil {
		t.Fatal(err)
	}
	call.Key += "-final"
	finalID, err := r.service.ToolNotifyUser(ctx, call, engine.NotifyUserArgs{Content: "Reviewed conclusion"})
	if err != nil {
		t.Fatal(err)
	}
	call.Key += "-duplicate"
	duplicate, err := r.service.ToolNotifyUser(ctx, call, engine.NotifyUserArgs{Content: "Do not duplicate"})
	if err != nil || duplicate != finalID || progressID == finalID {
		t.Fatalf("duplicate final: existing=%s replay=%s err=%v", finalID, duplicate, err)
	}
	// Then only the reviewed conclusion is queued under the stable commitment key.
	final, err := r.q.GetOutboxMessage(ctx, finalID)
	if err != nil || final.Status != "pending" || final.DeliveredAt.Valid || final.Peer != "telegram:42" || final.Body != "Reviewed conclusion" {
		t.Fatalf("bad queued message: %+v, %v", final, err)
	}
}

func TestUserChannelFollowUpQuestionIsNotAConclusion(t *testing.T) {
	r := startNative(t, "echo")
	ctx := t.Context()
	_, call := followUpResult(t, r)
	_, err := r.service.ToolAskUser(ctx, call, engine.AskUserArgs{Question: "Which scope?", Options: []string{"A", "B"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.service.settle(ctx, call.SourceRequestID, engine.Outcome{Status: "done", Answer: "private draft"}); err != nil {
		t.Fatal(err)
	}
	row, err := r.q.GetOutboxByDedupKey(ctx, "question:"+call.SourceRequestID)
	if err != nil || row.Kind != "question" || row.Peer != "telegram:42" || !strings.Contains(row.Body, "Which scope?") || strings.Contains(row.Body, "private draft") {
		t.Fatalf("unexpected question delivery: %+v, %v", row, err)
	}
	f, err := r.q.GetChannelFollowUpByInput(ctx, call.SourceRequestID)
	if err != nil || f.State != "open" {
		t.Fatalf("question completed the commitment: %+v, %v", f, err)
	}
}

func TestUserChannelFollowUpCanStaySilent(t *testing.T) {
	r := startNative(t, "echo")
	ctx := t.Context()
	_, call := followUpResult(t, r)
	if err := r.service.ToolStaySilent(ctx, call, engine.StaySilentArgs{Reason: "No conclusion needed"}); err != nil {
		t.Fatal(err)
	}
	if err := r.service.settle(ctx, call.SourceRequestID, engine.Outcome{Status: "done", Answer: "private draft"}); err != nil {
		t.Fatal(err)
	}
	f, err := r.q.GetChannelFollowUpByInput(ctx, call.SourceRequestID)
	if err != nil || f.State != "silent" {
		t.Fatalf("missing intentional silence: %+v, %v", f, err)
	}
	if _, err := r.q.GetOutboxByDedupKey(ctx, followUpFinalKey(f.DelegationID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("silence generated delivery: %v", err)
	}
}

func TestUserChannelFollowUpCancellationRevokesQueuedConclusion(t *testing.T) {
	for _, cancel := range []string{"stop", "clear", "revocation"} {
		t.Run(cancel, func(t *testing.T) {
			r := startNative(t, "echo")
			ctx := t.Context()
			parent, call := followUpResult(t, r)
			outboxID, err := r.service.ToolNotifyUser(ctx, call, engine.NotifyUserArgs{Content: "Do not send after cancellation"})
			if err != nil {
				t.Fatal(err)
			}
			if err := r.service.settle(ctx, call.SourceRequestID, engine.Outcome{Status: "done"}); err != nil {
				t.Fatal(err)
			}
			switch cancel {
			case "stop":
				err = r.service.Stop(ctx, "bot", parent)
			case "clear":
				err = r.service.Clear(ctx, "bot", parent, "reset-follow-up", "web", "")
			case "revocation":
				_, err = r.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{AgentID: "bot", Token: "123:fixture", AllowedUsers: "99", Options: "{}"})
				if err == nil {
					_, err = r.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{AgentID: "bot", Token: "123:fixture", AllowedUsers: "42", Options: "{}"})
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			// The delivery worker may have loaded this row before cancellation.
			if err := r.q.MarkOutboxDelivered(ctx, sqlc.MarkOutboxDeliveredParams{ID: outboxID, DeliveredAt: sql.NullInt64{Int64: 42, Valid: true}}); err != nil {
				t.Fatal(err)
			}
			row, err := r.q.GetOutboxMessage(ctx, outboxID)
			if err != nil || row.Status != "revoked" || row.DeliveredAt.Valid {
				t.Fatalf("cancelled follow-up can still send: %+v, %v", row, err)
			}
			if _, err := r.service.ToolNotifyUser(ctx, call, engine.NotifyUserArgs{Content: "Replay after cancellation"}); err == nil {
				t.Fatal("cancelled result queued again")
			}
		})
	}
}

func TestUserWebDelegationCannotOptInToChannelFollowUp(t *testing.T) {
	r := startNative(t, "echo")
	_, call := taskParent(t, r)
	if _, err := r.service.ToolSessionNew(t.Context(), call, engine.SessionNewArgs{Title: "Web-only", Message: "work", FollowUp: true}); !errors.Is(err, errChannelFollowUp) {
		t.Fatalf("web opt-in result: %v", err)
	}
}

func TestUserRePairingDoesNotReviveAnAcknowledgedChannelFollowUp(t *testing.T) {
	for _, platform := range []string{"telegram", "feishu", "wechat"} {
		t.Run(platform, func(t *testing.T) {
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
			// Given the original acknowledgment was sent while the child still had work to do.
			call.SourceRequestID = "channel-request"
			if _, err := r.q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{ID: call.SourceRequestID, ConversationID: parent, Content: "Please report", Attachments: "null", Origin: platform + ":42", Kind: "message", SourceKey: sql.NullString{String: platform + ":123:1", Valid: true}, ChannelContext: `{"accountId":"123","senderId":"42"}`}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.service.ToolSessionNew(ctx, call, engine.SessionNewArgs{Title: "Worker", Message: "child findings", FollowUp: true}); err != nil {
				t.Fatal(err)
			}
			if err := r.service.settle(ctx, call.SourceRequestID, engine.Outcome{Status: "done", Answer: "I will report back"}); err != nil {
				t.Fatal(err)
			}
			if err := r.q.MarkChannelReplyDelivered(ctx, sqlc.MarkChannelReplyDeliveredParams{ID: call.SourceRequestID, DeliveredAt: sql.NullInt64{Int64: 42, Valid: true}}); err != nil {
				t.Fatal(err)
			}
			// When permission is revoked and restored before the original child result exists.
			change("99")
			change("42")
			turn := turnID(call.Key)
			if err := r.service.admit(ctx, taskInput(t, r, turn)); err != nil {
				t.Fatal(err)
			}
			awaitReceipt(t, r, turn)
			// Then re-pairing does not authorize an explicit action from the old result.
			call.SourceRequestID = "result:" + turn
			call.Key += "/notify"
			if _, err := r.service.ToolNotifyUser(ctx, call, engine.NotifyUserArgs{Content: "Reviewed conclusion"}); !errors.Is(err, errChannelFollowUp) {
				t.Fatalf("old grant was revived: %v", err)
			}
			if _, err := r.q.GetOutboxByDedupKey(ctx, followUpFinalKey(turn)); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("unexpected final delivery: %v", err)
			}
		})
	}
}
