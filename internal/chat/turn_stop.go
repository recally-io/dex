package chat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// Stop cancels the observed task on Main. Without a target, other writable
// sessions use session cancellation.
func (s *Service) Stop(ctx context.Context, agentID string, id int64, target *string) error {
	c, err := s.Get(ctx, agentID, id)
	if err != nil {
		return err
	}
	if !c.Writable {
		return ErrNotFound
	}
	if c.ChannelKey != MainKey {
		if target != nil {
			return engine.ErrStaleRunTarget
		}
		return s.CancelSession(ctx, agentID, id)
	}
	if target == nil || *target == "" {
		return fmt.Errorf("%w: Main Stop requires a task target", engine.ErrInvalidInput)
	}
	if c.ExecutionID == 0 {
		return engine.ErrStaleRunTarget
	}
	unlock, err := s.lockSession(ctx, id)
	if err != nil {
		slog.InfoContext(ctx, "session stop", "session", id, "phase", "stop_wait_ended", "reason", admissionRetryReason(err))
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
	ref := engine.Ref{AgentID: agentID, ID: c.ExecutionID}
	trace := slog.With("session", id, "agent", agentID, "native_conversation_id", ref.ID)
	if err := s.engine.ValidateRunTarget(ctx, ref, *target); err != nil {
		trace.InfoContext(ctx, "session stop", "phase", "stop_rejected", "reason", admissionRetryReason(err))
		return err
	}
	// The intent covers a crash between the native abort mark and the Inbox boundary.
	s.admissionMu.RLock()
	if s.leaseUntil.Load() != 0 {
		s.admissionMu.RUnlock()
		return engine.ErrMaintenance
	}
	err = s.q.RecordMainTurnStop(ctx, sqlc.RecordMainTurnStopParams{ConversationID: id, Target: *target})
	s.admissionMu.RUnlock()
	if err != nil {
		return err
	}
	trace.InfoContext(ctx, "session stop", "phase", "stop_recorded", "target", *target)
	if err := s.applyMainStop(ctx, id, ref, *target); err != nil {
		if errors.Is(err, engine.ErrStaleRunTarget) {
			return err
		}
		s.retrySession(ctx, id)
		if !errors.Is(err, engine.ErrModelsPending) && !errors.Is(err, engine.ErrMaintenance) {
			return err
		}
	}
	return nil
}

func (s *Service) reconcileMainStops(ctx context.Context, id int64, ref engine.Ref) error {
	targets, err := s.q.ListMainTurnStops(ctx, id)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := s.applyMainStop(ctx, id, ref, target); err != nil && !errors.Is(err, engine.ErrStaleRunTarget) {
			return err
		}
	}
	return nil
}

func (s *Service) applyMainStop(ctx context.Context, id int64, ref engine.Ref, target string) error {
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Second)
	defer cancel()
	result := engine.ErrStaleRunTarget
	if ref.ID > 0 {
		result = s.engine.Stop(callCtx, ref, &target)
		if errors.Is(result, engine.ErrNotFound) {
			result = engine.ErrStaleRunTarget
		}
		if result != nil && !errors.Is(result, engine.ErrStaleRunTarget) {
			slog.InfoContext(ctx, "session stop", "session", id, "phase", "stop_reconcile", "reason", admissionRetryReason(result))
			return result
		}
	}
	if err := s.q.FinishMainTurnStop(callCtx, sqlc.FinishMainTurnStopParams{ConversationID: id, Target: target}); err != nil {
		slog.InfoContext(ctx, "session stop", "session", id, "phase", "stop_receipt_pending", "reason", admissionRetryReason(err))
		return err
	}
	return result
}
