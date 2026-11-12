package chat

import (
	"context"
	"database/sql"
	"errors"
)

// ChatOfTurn returns the session that holds input id, or ErrNotFound. The ID is the input ID of a
// turn: a result names its child session turn, and a task from before child sessions used the ID
// of its first input as its own ID. The session belongs to agentID, or is a child of a session of
// agentID in another bot (ADR 0016). The child names its bot in AgentID.
func (s *Service) ChatOfTurn(ctx context.Context, agentID, id string) (Chat, error) {
	turn, err := s.q.GetSubmission(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, ErrNotFound
	}
	if err != nil {
		return Chat{}, err
	}
	conv, err := s.q.GetConversation(ctx, turn.ConversationID)
	if err != nil {
		return Chat{}, err
	}
	if conv.AgentID == agentID || !conv.ParentID.Valid {
		return s.Get(ctx, agentID, turn.ConversationID)
	}
	parent, err := s.q.GetConversation(ctx, conv.ParentID.Int64)
	if err != nil || parent.AgentID != agentID {
		return Chat{}, ErrNotFound
	}
	c, err := s.Get(ctx, conv.AgentID, conv.ID)
	if err != nil {
		return Chat{}, err
	}
	agent, err := s.q.GetAgent(ctx, conv.AgentID)
	if err != nil {
		return Chat{}, err
	}
	c.AgentID, c.AgentName = agent.ID, agent.Name
	return c, nil
}
