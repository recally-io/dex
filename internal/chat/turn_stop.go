package chat

import (
	"context"
	"errors"
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
	unlock, err := s.lockAgent(ctx, agentID)
	if err != nil {
		return err
	}
	defer func() { unlock(); s.Wake() }()
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
		s.scheduleRetry()
		return err
	}
	return nil
}

// reconcileMainStops runs under the existing agent gate, after legacy aborts.
func (s *Service) reconcileMainStops(ctx context.Context, agentID string, blocked map[int64]bool) error {
	targets, err := s.q.ListAgentMainTurnStops(ctx, agentID)
	if err != nil {
		return err
	}
	var errs []error
	for _, stop := range targets {
		if blocked[stop.ConversationID] {
			continue
		}
		ref := engine.Ref{AgentID: agentID, ID: stop.ExecutionID.Int64, Scope: stop.ExecutionScope}
		if err := s.applyMainStop(ctx, stop.ConversationID, ref, stop.Target); err != nil {
			blocked[stop.ConversationID] = true
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) applyMainStop(ctx context.Context, id int64, ref engine.Ref, target string) error {
	callCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if err := s.engine.StopRun(callCtx, ref, target); err != nil {
		return err
	}
	return s.q.FinishMainTurnStop(callCtx, sqlc.FinishMainTurnStopParams{ConversationID: id, Target: target})
}
