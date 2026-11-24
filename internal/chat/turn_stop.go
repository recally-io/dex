package chat

import (
	"context"
	"fmt"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// StopWeb cancels only the observed foreground run on Main. Other writable sessions keep legacy Stop.
func (s *Service) StopWeb(ctx context.Context, agentID string, id int64, target string) error {
	c, err := s.Get(ctx, agentID, id)
	if err != nil {
		return err
	}
	if !c.Writable {
		return ErrNotFound
	}
	if c.ChannelKey != MainKey {
		if target != "" {
			return engine.ErrStaleRunTarget
		}
		return s.Stop(ctx, agentID, id)
	}
	if target == "" {
		return fmt.Errorf("%w: Main Stop requires a run target", engine.ErrInvalidInput)
	}
	if c.ExecutionID == 0 {
		return engine.ErrStaleRunTarget
	}
	unlock, err := s.lockSession(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	conv, err := s.q.GetConversation(ctx, id)
	if err != nil {
		return err
	}
	if conv.AbortPending != 0 {
		return engine.ErrStaleRunTarget
	}
	ref := engine.Ref{AgentID: agentID, ID: c.ExecutionID, Scope: c.ExecutionScope}
	if err := s.engine.ValidateRunTarget(ctx, ref, target); err != nil {
		return err
	}
	// The intent covers a crash between the native abort mark and the Inbox boundary.
	s.admissionMu.RLock()
	if s.leaseUntil.Load() != 0 {
		s.admissionMu.RUnlock()
		return engine.ErrMaintenance
	}
	err = s.q.RecordMainTurnStop(ctx, sqlc.RecordMainTurnStopParams{ConversationID: id, Target: target})
	s.admissionMu.RUnlock()
	if err != nil {
		return err
	}
	if err := s.applyMainStop(ctx, id, ref, target); err != nil {
		s.retrySession(id)
		return err
	}
	return nil
}

func (s *Service) reconcileMainStops(ctx context.Context, id int64, ref engine.Ref) error {
	targets, err := s.q.ListMainTurnStops(ctx, id)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := s.applyMainStop(ctx, id, ref, target); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) applyMainStop(ctx context.Context, id int64, ref engine.Ref, target string) error {
	callCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if err := s.engine.StopRun(callCtx, ref, target); err != nil {
		return err
	}
	return s.q.FinishMainTurnStop(callCtx, sqlc.FinishMainTurnStopParams{ConversationID: id, Target: target})
}
