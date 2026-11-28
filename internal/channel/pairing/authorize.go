package pairing

import (
	"context"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// RequirePairedChannel checks that the account and peer have a current binding.
func RequirePairedChannel(ctx context.Context, q *sqlc.Queries, botID, peer, accountID string) error {
	paired, err := q.ChannelPaired(ctx, sqlc.ChannelPairedParams{BotID: botID, Peer: peer, AccountID: accountID})
	if err == nil && !paired {
		return errors.New("channel is not paired")
	}
	return err
}
