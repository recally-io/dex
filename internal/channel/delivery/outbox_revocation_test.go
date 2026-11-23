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
	beforeSend func()
	sends      int
}

func (*revokeBeforeSendBot) Run(ctx context.Context) error { return ctx.Err() }
func (b *revokeBeforeSendBot) Parts(reply channel.Reply) []channel.Part {
	b.beforeSend()
	return []channel.Part{{Text: reply.Answer}}
}
func (b *revokeBeforeSendBot) Send(context.Context, string, string, int, channel.Part) (channel.Receipt, error) {
	b.sends++
	return channel.Receipt{MessageIDs: []string{"synthetic-receipt"}}, nil
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
	if _, err := q.InsertOutboxMessage(ctx, sqlc.InsertOutboxMessageParams{ID: id, DedupKey: id, AgentID: "fixture-bot", ConversationID: conv.ID, SourceSubmissionID: source, Kind: "proactive", Peer: "wechat:fixture-peer", Body: "Reviewed conclusion", Status: "pending"}); err != nil {
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
	if err != nil || bot.sends != 0 || row.Status != "revoked" || row.DeliveredAt.Valid || row.DeliveredParts != 0 || row.RemoteIds != "[]" {
		t.Fatalf("unsent row was treated as a delivery: sends=%d status=%s deliveredAt=%v parts=%d remote=%s err=%v", bot.sends, row.Status, row.DeliveredAt, row.DeliveredParts, row.RemoteIds, err)
	}
}
