package session

import (
	"context"
	"database/sql"
	"errors"
)

// SourceReceipt describes stored business input, including input awaiting native admission.
// Staged means an album or speech receipt owns uploads but has no native input yet.
type SourceReceipt struct {
	ID        string
	SessionID int64
	Pending   bool // A pending input for typing status.
	Staged    bool
}

// ReceivedSource reports whether a global platform source is already stored.
// A bot credential can move between bots without changing its source identities.
func (s *Service) ReceivedSource(ctx context.Context, source string) (bool, error) {
	_, err := s.q.GetSubmissionBySource(ctx, sql.NullString{String: source, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		_, err = s.q.GetTelegramAlbumMember(ctx, source)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
	}
	return err == nil, err
}

// LookupSource finds stored input for a bot. It rejects another bot's source.
func (s *Service) LookupSource(ctx context.Context, botID, source string) (SourceReceipt, error) {
	id, err := s.q.GetSubmissionBySource(ctx, sql.NullString{String: source, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		member, lookupErr := s.q.GetTelegramAlbumMember(ctx, source)
		if errors.Is(lookupErr, sql.ErrNoRows) {
			effect, effectErr := s.q.GetTelegramEffect(ctx, "stt:"+source)
			if errors.Is(effectErr, sql.ErrNoRows) {
				return SourceReceipt{}, nil
			}
			if effectErr != nil {
				return SourceReceipt{}, effectErr
			}
			if effect.BotID != botID {
				return SourceReceipt{}, ErrSessionNotFound
			}
			return SourceReceipt{Staged: effect.InputJson != "{}"}, nil
		}
		if lookupErr != nil {
			return SourceReceipt{}, lookupErr
		}
		if member.BotID != botID {
			return SourceReceipt{}, ErrSessionNotFound
		}
		return SourceReceipt{Staged: true}, nil
	}
	if err != nil {
		return SourceReceipt{}, err
	}
	sub, err := s.q.GetSubmission(ctx, id)
	if err != nil {
		return SourceReceipt{}, err
	}
	if _, err := s.Get(ctx, botID, sub.SessionID); err != nil {
		return SourceReceipt{}, err
	}
	return SourceReceipt{ID: id, SessionID: sub.SessionID, Pending: sub.Status == "pending" && sub.ChannelRevoked == 0}, nil
}
