package chat

import (
	"context"
	"errors"

	"github.com/recally-io/dex/internal/engine"
)

// ErrNothingToContinue means that the chat has never run, so there is no run to continue.
var ErrNothingToContinue = errors.New("the chat has no run to continue")

// Continue runs a writable chat again on its current context, for example after a failed or
// stopped run. It sends no new message. A retry with the same request ID starts nothing new. It
// returns engine.ErrBusy while the chat runs.
func (s *Service) Continue(ctx context.Context, agentID string, chatID int64, requestID string) error {
	c, err := s.Get(ctx, agentID, chatID)
	if err != nil {
		return err
	}
	if !c.Writable {
		return ErrNotFound
	}
	if s.leaseUntil.Load() != 0 {
		return engine.ErrMaintenance
	}
	conv, err := s.q.GetConversation(ctx, chatID)
	if err != nil {
		return err
	}
	if !conv.ExecutionID.Valid {
		return ErrNothingToContinue
	}
	agent, err := s.q.GetAgent(ctx, agentID)
	if err != nil {
		return err
	}
	err = s.engine.Continue(ctx, engine.Ref{AgentID: agentID, ID: conv.ExecutionID.Int64}, requestID, chatOptions(agent, conv))
	if err == nil {
		s.signal(chatID)
	}
	return err
}
