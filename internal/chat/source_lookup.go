package chat

import (
	"context"
	"database/sql"
	"errors"
)

// SourceReceipt describes stored business input, including input awaiting native admission.
// An empty ID means that the source has no receipt.
type SourceReceipt struct {
	ID      string
	ChatID  int64
	Pending bool
}

// ReceivedSource reports whether a global platform source is already stored.
// A bot credential can move between agents without changing its source identities.
func (s *Service) ReceivedSource(ctx context.Context, source string) (bool, error) {
	_, err := s.q.GetSubmissionBySource(ctx, sql.NullString{String: source, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// LookupSource finds stored input for an agent. It rejects another agent's source.
func (s *Service) LookupSource(ctx context.Context, agentID, source string) (SourceReceipt, error) {
	id, err := s.q.GetSubmissionBySource(ctx, sql.NullString{String: source, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return SourceReceipt{}, nil
	}
	if err != nil {
		return SourceReceipt{}, err
	}
	sub, err := s.q.GetSubmission(ctx, id)
	if err != nil {
		return SourceReceipt{}, err
	}
	if _, err := s.Get(ctx, agentID, sub.ConversationID); err != nil {
		return SourceReceipt{}, err
	}
	return SourceReceipt{ID: id, ChatID: sub.ConversationID, Pending: sub.Status == "pending"}, nil
}
