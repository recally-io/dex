package chat

import (
	"context"
	"database/sql"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
)

var errChannelFollowUp = errors.New("follow-up requires an active commitment and the original channel authorization")

const followUpPrompt = "You committed to report on this delegation. Plain final text is internal. Use notify_user for your reviewed conclusion, or ask_user if blocked on a decision. Do not forward private drafts or raw child output."

func followUpFinalKey(delegationID string) string { return "follow-up-final:" + delegationID }

func createChannelFollowUp(ctx context.Context, q *sqlc.Queries, parent sqlc.Conversation, source sqlc.Submission, turn string) error {
	if source.Kind != "message" || !isUserOrigin(source.Origin) || peerFromOrigin(source.Origin) == "" || !source.SourceKey.Valid || source.ChannelRevoked != 0 || parent.ExecutionScope != "" {
		return errChannelFollowUp
	}
	return q.CreateChannelFollowUp(ctx, sqlc.CreateChannelFollowUpParams{DelegationID: turn, ResultID: "result:" + turn, ConversationID: parent.ID})
}

// cancelChannelFollowUps runs inside the Stop or Clear transaction. Outbox revocation
// prevents a future send; a worker may still record a receipt for a send already in flight.
func cancelChannelFollowUps(ctx context.Context, q *sqlc.Queries, chatID int64) error {
	ids, err := q.CancelChannelFollowUps(ctx, chatID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := q.RevokeOutboxBySubmission(ctx, sql.NullString{String: id, Valid: true}); err != nil {
			return err
		}
	}
	return nil
}
