package chat

import (
	"context"
	"database/sql"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// ChannelInputBinding freezes a reply route for an adapter-verified private input.
func ChannelInputBinding(ctx context.Context, q *sqlc.Queries, agentID, peer string, actor ChannelContext) (sql.NullInt64, error) {
	ownerPeer := peer
	if strings.HasPrefix(peer, "telegram:") {
		ownerPeer = "telegram:" + actor.SenderID
		if peer != ownerPeer {
			if err := q.EnsureTelegramReplyBinding(ctx, sqlc.EnsureTelegramReplyBindingParams{AgentID: agentID, AccountID: actor.AccountID, Peer: peer, OwnerPeer: ownerPeer}); err != nil {
				return sql.NullInt64{}, err
			}
		}
	}
	binding, err := q.GetActiveChannelBinding(ctx, sqlc.GetActiveChannelBindingParams{AgentID: agentID, AccountID: actor.AccountID, Peer: peer, OwnerPeer: ownerPeer})
	return sql.NullInt64{Int64: binding.ID, Valid: err == nil}, err
}
