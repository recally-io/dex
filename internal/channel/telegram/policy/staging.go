package policy

import (
	"context"
	"database/sql"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/session"
)

// LookupStagedSourceIn separates durable member receipts from speech claims.
// A speech claim can own a file before its update is safe to acknowledge.
func LookupStagedSourceIn(ctx context.Context, q *sqlc.Queries, source string, includeUnreceived bool) (session.StagedSource, error) {
	member, err := q.GetTelegramAlbumMember(ctx, source)
	if err == nil {
		staged := session.StagedSource{BotID: member.BotID, Received: true, OwnsUploads: true}
		// The batch receipt owns the file as soon as it commits, even if Finish has not run.
		if includeUnreceived && member.BatchSource != "" {
			staged.SubmissionID, err = session.SubmissionSourceIn(ctx, q, member.BatchSource)
		}
		return staged, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return session.StagedSource{}, err
	}
	if !includeUnreceived {
		return session.StagedSource{}, nil
	}
	effect, err := q.GetTelegramEffect(ctx, "stt:"+source)
	if errors.Is(err, sql.ErrNoRows) {
		return session.StagedSource{}, nil
	}
	if err != nil {
		return session.StagedSource{}, err
	}
	return session.StagedSource{BotID: effect.BotID, OwnsUploads: effect.InputJson != "{}"}, nil
}

// CheckInputIn checks that staged input is still valid while Session holds the writer.
func CheckInputIn(ctx context.Context, q *sqlc.Queries, botID, peer string, input session.Message) error {
	if input.SpeechSource != "" {
		effect, err := q.GetTelegramEffect(ctx, input.SpeechSource)
		if err != nil {
			return err
		}
		if effect.BotID != botID || effect.State == "discarded" {
			return errors.New("speech input discarded before admission")
		}
	}
	if input.AlbumSource != "" {
		sealed, err := q.IsTelegramAlbumSealed(ctx, sqlc.IsTelegramAlbumSealedParams{
			BotID: botID, BatchSource: input.AlbumSource, Peer: peer,
			AccountID: input.ChannelContext.AccountID, SenderID: input.ChannelContext.SenderID,
		})
		if err != nil {
			return err
		}
		if !sealed {
			return errors.New("album was discarded before admission")
		}
	}
	return nil
}

// DiscardStagedIn revokes staged Main input in the same transaction as Stop or Clear.
func DiscardStagedIn(ctx context.Context, q *sqlc.Queries, botID string) error {
	if err := q.DiscardTelegramInputEffects(ctx, botID); err != nil {
		return err
	}
	return q.DiscardPrivateTelegramAlbums(ctx, botID)
}
