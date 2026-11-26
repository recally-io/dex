package session

import (
	"context"
	"errors"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// RequirePairedChannel checks the current private sender at intake.
func RequirePairedChannel(ctx context.Context, q *sqlc.Queries, botID, peer string, actor ChannelContext) error {
	if strings.HasPrefix(peer, "telegram:") {
		return authorizeTelegramActor(ctx, q, botID, actor.AccountID, actor.SenderID)
	}
	paired, err := q.ChannelPaired(ctx, sqlc.ChannelPairedParams{BotID: botID, Peer: peer, AccountID: actor.AccountID})
	if err == nil && !paired {
		return errors.New("channel is not paired")
	}
	return err
}
