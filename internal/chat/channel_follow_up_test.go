package chat

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

func followUpSource(t *testing.T, r *nativeTest) (int64, engine.TaskContext) {
	t.Helper()
	ctx := t.Context()
	parent, call := taskParent(t, r)
	call.SourceRequestID = "channel-request"
	if _, err := r.q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{AgentID: "bot", Token: "123:fixture", AllowedUsers: "42", Options: "{}"}); err != nil {
		t.Fatal(err)
	}
	bindingID, err := ChannelInputBinding(ctx, r.q, "bot", "telegram:42", ChannelContext{AccountID: "123", SenderID: "42"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{BindingID: bindingID, ID: call.SourceRequestID, ConversationID: parent, Content: "Please report back", Attachments: "null", SourceKey: sql.NullString{String: "telegram:123:1", Valid: true}, Origin: "telegram:42", Kind: "message", ChannelContext: `{"accountId":"123","senderId":"42"}`}); err != nil {
		t.Fatal(err)
	}
	return parent, call
}

func followUpResult(t *testing.T, r *nativeTest) (int64, engine.TaskContext) {
	t.Helper()
	ctx := t.Context()
	parent, call := followUpSource(t, r)
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
	var wg sync.WaitGroup
	var ids [2]string
	start := make(chan struct{})
	for i := range ids {
		wg.Go(func() {
			<-start
			invocation := call
			invocation.Key += string(rune('a' + i))
			var err error
			ids[i], err = r.service.ToolNotifyUser(ctx, invocation, engine.NotifyUserArgs{Content: "Reviewed conclusion"})
			if err != nil {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	finalID := ids[0]
	if finalID == "" || ids[1] != finalID || progressID == finalID {
		t.Fatalf("concurrent finals = %v, progress = %s", ids, progressID)
	}
	call.Key += "-replay"
	if replay, err := r.service.ToolNotifyUser(ctx, call, engine.NotifyUserArgs{Content: "Do not duplicate"}); err != nil || replay != finalID {
		t.Fatalf("replay final = %s want %s, err=%v", replay, finalID, err)
	}
	// Then only the reviewed conclusion is queued under the stable commitment key.
	final, err := r.q.GetOutboxMessage(ctx, finalID)
	if err != nil || final.Status != "pending" || final.DeliveredAt.Valid || final.Peer != "telegram:42" || final.Body != "Reviewed conclusion" {
		t.Fatalf("bad queued message: %+v, %v", final, err)
	}
}

func TestUserLegacyChannelFollowUpWithoutParentCannotNotify(t *testing.T) {
	r := startNative(t, "echo")
	ctx := t.Context()
	_, call := followUpResult(t, r)
	if _, err := r.conn.ExecContext(ctx, `UPDATE channel_follow_ups SET parent_request_id=NULL, parent_source_key=NULL WHERE result_id=?`, call.SourceRequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.ToolNotifyUser(ctx, call, engine.NotifyUserArgs{Content: "No authorized parent"}); !errors.Is(err, errChannelFollowUp) {
		t.Fatalf("legacy follow-up error = %v, want %v", err, errChannelFollowUp)
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

func TestUserChannelFollowUpWithoutNotificationKeepsItsOutcome(t *testing.T) {
	for _, tc := range []struct{ state, want string }{
		{"open", "missed_follow_up"},
		{"silent", "silent"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			r := startNative(t, "echo")
			ctx := t.Context()
			parent, call := followUpResult(t, r)
			// Older releases could save an explicit silent decision before the turn settled.
			if tc.state == "silent" {
				if err := r.q.SetOpenChannelFollowUpState(ctx, sqlc.SetOpenChannelFollowUpStateParams{ResultID: call.SourceRequestID, State: tc.state}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := r.q.ListOutboxByConversation(ctx, parent)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.service.settle(ctx, call.SourceRequestID, engine.Outcome{Status: "done", Answer: "private draft"}); err != nil {
				t.Fatal(err)
			}
			f, err := r.q.GetChannelFollowUpByInput(ctx, call.SourceRequestID)
			if err != nil || f.State != tc.want {
				t.Fatalf("follow-up state = %q, want %q: %v", f.State, tc.want, err)
			}
			after, err := r.q.ListOutboxByConversation(ctx, parent)
			if err != nil || len(after) != len(before) {
				t.Fatalf("settlement without a notification added an outbox entry: %v", err)
			}
		})
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
				err = r.service.CancelSession(ctx, "bot", parent)
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

			row, err := r.q.GetOutboxMessage(ctx, outboxID)
			if err != nil || row.Status != "revoked" || row.DeliveredAt.Valid {
				t.Fatalf("cancelled follow-up can still send: %+v, %v", row, err)
			}
			replay, err := r.service.ToolNotifyUser(ctx, call, engine.NotifyUserArgs{Content: "Replay after cancellation"})
			if cancel == "revocation" {
				if err != nil || replay != outboxID {
					t.Fatalf("replay did not keep the revoked delivery: %q, %v", replay, err)
				}
			} else if err == nil {
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
