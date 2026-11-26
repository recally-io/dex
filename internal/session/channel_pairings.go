package session

import (
	"context"
	"errors"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// RequirePairedChannel checks the current private sender at intake.
func RequirePairedChannel(ctx context.Context, q *sqlc.Queries, agentID, peer string, actor ChannelContext) error {
	if strings.HasPrefix(peer, "telegram:") {
		return authorizeTelegramActor(ctx, q, agentID, actor.AccountID, actor.SenderID)
	}
	paired, err := q.ChannelPaired(ctx, sqlc.ChannelPairedParams{AgentID: agentID, Peer: peer, AccountID: actor.AccountID})
	if err == nil && !paired {
		return errors.New("channel is not paired")
	}
	return err
}
