package session

import (
	"context"
	"database/sql"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// SourceReceipt describes stored business input, including input awaiting native admission.
// Staged means a channel adapter owns uploads outside the business input.
type SourceReceipt struct {
	ID        string
	SessionID int64
	Pending   bool // A pending input for typing status.
	Staged    bool
}

// StagedSource describes an adapter's receipt. A stage can own uploads without
// confirming receipt of the source. A committed input can own several sources.
type StagedSource struct {
	BotID        string
	SubmissionID string // Set when a business submission owns this source.
	Received     bool
	OwnsUploads  bool
}

// SubmissionSourceIn finds the global business receipt with the caller's queries.
// An empty ID means no receipt exists. A staged source does not count here.
func SubmissionSourceIn(ctx context.Context, q *sqlc.Queries, source string) (string, error) {
	id, err := q.GetSubmissionBySource(ctx, sql.NullString{String: source, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ReceivedSource reports whether a global platform source is already stored.
// A bot credential can move between bots without changing its source identities.
func (s *Service) ReceivedSource(ctx context.Context, source string) (bool, error) {
	id, err := SubmissionSourceIn(ctx, s.q, source)
	if err != nil || id != "" || s.lookupStagedSourceIn == nil {
		return id != "", err
	}
	staged, err := s.lookupStagedSourceIn(ctx, s.q, source, false)
	return staged.Received, err
}

// LookupSource finds stored input for a bot. It rejects another bot's source.
func (s *Service) LookupSource(ctx context.Context, botID, source string) (SourceReceipt, error) {
	id, err := SubmissionSourceIn(ctx, s.q, source)
	if err != nil {
		return SourceReceipt{}, err
	}
	if id == "" {
		if s.lookupStagedSourceIn == nil {
			return SourceReceipt{}, nil
		}
		staged, err := s.lookupStagedSourceIn(ctx, s.q, source, true)
		if err != nil {
			return SourceReceipt{}, err
		}
		if staged.BotID != "" && staged.BotID != botID {
			return SourceReceipt{}, ErrSessionNotFound
		}
		if staged.SubmissionID == "" {
			return SourceReceipt{Staged: staged.OwnsUploads}, nil
		}
		id = staged.SubmissionID
	}
	sub, err := s.q.GetSubmission(ctx, id)
	if err != nil {
		return SourceReceipt{}, err
	}
	if _, err := s.Get(ctx, botID, sub.SessionID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return SourceReceipt{}, ErrSessionNotFound
		}
		return SourceReceipt{}, err
	}
	return SourceReceipt{ID: id, SessionID: sub.SessionID, Pending: sub.Status == "pending" && sub.ChannelRevoked == 0}, nil
}
