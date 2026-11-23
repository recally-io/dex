package chat

import (
	"context"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
)

var errChannelFollowUp = errors.New("follow-up requires an active commitment and the original channel authorization")

const followUpPrompt = "You committed to report on this delegation. Plain final text is internal. Use notify_user for your reviewed conclusion, ask_user if blocked on a decision, or stay_silent to intentionally suppress the follow-up. Do not forward private drafts or raw child output."

func followUpFinalKey(delegationID string) string { return "follow-up-final:" + delegationID }

func createChannelFollowUp(ctx context.Context, q *sqlc.Queries, parent sqlc.Conversation, source sqlc.Submission, turn string) error {
	created, err := q.CreateChannelFollowUp(ctx, sqlc.CreateChannelFollowUpParams{
		DelegationID: turn, ResultID: "result:" + turn, ConversationID: parent.ID, SourceID: source.ID,
	})
	if err == nil && created == 0 {
		return errChannelFollowUp
	}
	return err
}
