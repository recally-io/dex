package chat

import (
	"context"
	"database/sql"
	"errors"
)

// ChatOfTurn returns the session that holds input id of agentID, or ErrNotFound. The ID is the
// input ID of a turn: a result names its child session turn, and a task from before child
// sessions used the ID of its first input as its own ID.
func (s *Service) ChatOfTurn(ctx context.Context, agentID, id string) (Chat, error) {
	turn, err := s.q.GetSubmission(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, ErrNotFound
	}
	if err != nil {
		return Chat{}, err
	}
	return s.Get(ctx, agentID, turn.ConversationID)
}
