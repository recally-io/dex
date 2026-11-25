package delivery

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

type revokeBeforeSendBot struct {
	beforeSend    func()
	beforeReceipt func()
	sends         int
}

func (*revokeBeforeSendBot) Run(ctx context.Context) error { return ctx.Err() }
func (b *revokeBeforeSendBot) Parts(reply channel.Reply) []channel.Part {
	if b.beforeSend != nil {
		b.beforeSend()
	}
	return []channel.Part{{Text: reply.Answer}}
}
func (b *revokeBeforeSendBot) Send(context.Context, string, string, channel.Part) (channel.Receipt, error) {
	b.sends++
	if b.beforeReceipt != nil {
		b.beforeReceipt()
	}
	return channel.Receipt{MessageID: "synthetic-receipt"}, nil
}

func pendingTelegramConclusion(t *testing.T) (*Manager, *sql.DB) {
	t.Helper()
	ctx := t.Context()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	manager := New(ctx, conn, nil, "fixture-bot")
	t.Cleanup(manager.Close)
	// Given a durable final conclusion authorized for the original Telegram receipt.
	if _, err := conn.ExecContext(ctx, `
INSERT INTO agents (id,name,model_provider,model_id,created_at) VALUES ('fixture-bot','Fixture','','',1);
INSERT INTO telegram_settings (agent_id,token,allowed_users) VALUES ('fixture-bot','123:fixture','42');
INSERT INTO conversations (id,agent_id,channel_key,title,created_at) VALUES (1,'fixture-bot','main','Main',1),(2,'fixture-bot','child','Child',1);
UPDATE conversations SET parent_id=1 WHERE id=2;
INSERT INTO submissions (id,conversation_id,content,status,source_key,origin,kind,channel_context,created_at) VALUES
    ('original',1,'Delegate','done','original-key','telegram:42','message','{"accountId":"123","senderId":"42"}',1),
    ('child-turn',2,'Do work','done',NULL,'task','message','{}',2),
    ('result:child-turn',1,'Canonical result','done',NULL,'telegram:42','task_result','{"accountId":"123","senderId":"42"}',3);
UPDATE submissions SET parent_request_id='original' WHERE id='child-turn';
INSERT INTO channel_follow_ups (delegation_id,result_id,conversation_id,child_id,parent_request_id,parent_source_key,state)
VALUES ('child-turn','result:child-turn',1,2,'original','original-key','notified');
INSERT INTO outbox_messages (id,event_id,agent_id,conversation_id,source_submission_id,kind,peer,channel_context,body,created_at)
VALUES ('final','follow-up-final:child-turn','fixture-bot',1,'result:child-turn','proactive','telegram:42','{"accountId":"123","senderId":"42"}','Reviewed conclusion',4);`); err != nil {
		t.Fatal(err)
	}
	return manager, conn
}

func TestUserRevokedFollowUpSnapshotNeverEntersTelegram(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	manager, conn := pendingTelegramConclusion(t)
	q := sqlc.New(conn)
	loaded := false
	// When the delivery loop has loaded the Outbox snapshot but has not
	// dispatched yet, revoke the saved channel route through the real writer.
	bot := &revokeBeforeSendBot{beforeSend: func() {
		loaded = true
		if _, err := q.SetTelegramSettings(ctx, sqlc.SetTelegramSettingsParams{AgentID: "fixture-bot", Token: "123:fixture", AllowedUsers: "99", Options: "{}"}); err != nil {
			t.Fatal(err)
		}
	}}
	if err := manager.flush(ctx, "telegram", bot); err != nil {
		t.Fatal(err)
	}
	var disposition, peer, remote string
	err := conn.QueryRowContext(ctx, `SELECT disposition,peer,remote_ids FROM outbox_delivery_diagnostics WHERE id='final'`).Scan(&disposition, &peer, &remote)
	if err != nil || !loaded || bot.sends != 0 || disposition != "revoked_or_skipped_unconfirmed" || peer != "telegram:42" || remote != "[]" {
		t.Fatalf("loaded=%t platform entries=%d disposition=%s peer=%s remote=%s err=%v", loaded, bot.sends, disposition, peer, remote, err)
	}
}

func TestUserFollowUpKeepsReturnedReceiptAfterSenderCancellation(t *testing.T) {
	t.Parallel()
	manager, conn := pendingTelegramConclusion(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// Revocation and a channel runtime replacement can cancel the delivery worker
	// at the same time a request that entered the platform returns its receipt.
	bot := &revokeBeforeSendBot{beforeReceipt: func() {
		q := sqlc.New(conn)
		if _, err := q.SetTelegramSettings(t.Context(), sqlc.SetTelegramSettingsParams{AgentID: "fixture-bot", Token: "123:fixture", AllowedUsers: "99", Options: "{}"}); err != nil {
			t.Fatal(err)
		}
		cancel()
	}}
	_ = manager.flush(ctx, "telegram", bot)
	if err := manager.flush(t.Context(), "telegram", bot); err != nil {
		t.Fatal(err)
	}
	var disposition, peer, remote string
	err := conn.QueryRowContext(t.Context(), `SELECT disposition,peer,coalesce(remote_ids -> 0 ->> 'id','') FROM outbox_delivery_diagnostics WHERE id='final'`).Scan(&disposition, &peer, &remote)
	if err != nil || bot.sends != 1 || disposition != "platform_confirmed" || peer != "telegram:42" || remote != "synthetic-receipt" {
		t.Fatalf("platform entries=%d disposition=%q peer=%q remote=%q err=%v", bot.sends, disposition, peer, remote, err)
	}
}

func TestUserRevokedOutboxSnapshotIsNotSentOrConfirmed(t *testing.T) {
	ctx := t.Context()
	conn, err := db.Open(ctx, filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	manager := New(ctx, conn, nil, "fixture-bot")
	defer manager.Close()
	q := sqlc.New(conn)
	if err := q.CreateAgentIfMissing(ctx, sqlc.CreateAgentIfMissingParams{ID: "fixture-bot", Name: "Fixture"}); err != nil {
		t.Fatal(err)
	}
	conv, err := q.EnsureConversation(ctx, sqlc.EnsureConversationParams{AgentID: "fixture-bot", ChannelKey: "main", Title: "Fixture"})
	if err != nil {
		t.Fatal(err)
	}
	const id = "fixture-result"
	source := sql.NullString{String: id, Valid: true}
	if _, err := q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{ID: id, ConversationID: conv.ID, Content: "Result", Attachments: "null", Origin: "wechat:fixture-peer", Kind: "task_result"}); err != nil {
		t.Fatal(err)
	}
	if err := q.SetWechatSettings(ctx, sqlc.SetWechatSettingsParams{AgentID: "fixture-bot", BotID: "fixture-bot", Owner: "fixture-peer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertOutboxMessage(ctx, sqlc.InsertOutboxMessageParams{ID: id, EventID: id, ChannelContext: `{"accountId":"fixture-bot"}`, AgentID: "fixture-bot", ConversationID: conv.ID, SourceSubmissionID: source, Kind: "proactive", Peer: "wechat:fixture-peer", Body: "Reviewed conclusion", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	// Stop revokes the row after the sender loads it but before any platform call.
	bot := &revokeBeforeSendBot{beforeSend: func() {
		if err := q.RevokeOutboxBySubmission(ctx, source); err != nil {
			t.Fatal(err)
		}
	}}
	if err := manager.flush(ctx, "wechat", bot); err != nil {
		t.Fatal(err)
	}
	row, err := q.GetOutboxMessage(ctx, id)
	if err != nil || bot.sends != 0 || row.Status != "failed" || row.DeliveredAt.Valid || row.RemoteIds != "[]" {
		t.Fatalf("unsent row was treated as a delivery: sends=%d status=%s deliveredAt=%v remote=%s err=%v", bot.sends, row.Status, row.DeliveredAt, row.RemoteIds, err)
	}
}
