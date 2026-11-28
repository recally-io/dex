package policy

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/session"
)

func (p *Policy) DecorateReply(ctx context.Context, r sqlc.OutboxMessage, part *channel.Part) error {
	if r.Kind != "question" || !r.SourceSubmissionID.Valid {
		return nil
	}
	question, err := p.q.GetQuestionBySource(ctx, r.SourceSubmissionID.String)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var identity session.ChannelContext
	if err := json.Unmarshal([]byte(r.ChannelContext), &identity); err != nil {
		return err
	}
	conv, err := p.q.GetSession(ctx, r.SessionID)
	if err != nil {
		return err
	}
	target := session.Session{ID: conv.ID, ExecutionID: conv.ExecutionID.Int64}
	actions, err := questionActions(question)
	if err != nil {
		return err
	}
	return p.addButtons(ctx, part, r.SourceSubmissionID.String, r.Peer, identity, target, actions)
}

func (*Policy) Confirmed(ctx context.Context, q *sqlc.Queries, r sqlc.OutboxMessage, part channel.Part, receipt channel.Receipt) error {
	if len(part.Buttons) == 0 {
		return nil
	}
	return q.BindTelegramCallbackMessage(ctx, sqlc.BindTelegramCallbackMessageParams{SourceID: cmp.Or(r.SourceSubmissionID.String, r.ID), MessageID: receipt.MessageID})
}
