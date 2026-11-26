package session

import (
	"context"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// ErrNothingToCompact means that the session has never run, so it has no context to compact.
var ErrNothingToCompact = errors.New("the session has no context to compact")

// ErrNothingToContinue means that the session has never run, so there is no run to continue.
var ErrNothingToContinue = errors.New("the session has no run to continue")

// Continue runs a writable session again on its current context, for example after a failed or
// stopped run. It sends no new message. A retry with the same request ID starts nothing new. It
// returns engine.ErrBusy while the session runs.
func (s *Service) Continue(ctx context.Context, botID string, sessionID int64, requestID string) error {
	c, err := s.Get(ctx, botID, sessionID)
	if err != nil {
		return err
	}
	if !c.Writable {
		return ErrNotFound
	}
	unlock, err := s.lockSession(ctx, sessionID)
	if err != nil {
		return err
	}
	defer unlock()
	if s.leaseUntil.Load() != 0 {
		return engine.ErrMaintenance
	}
	conv, err := s.q.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if conv.AbortPending != 0 {
		return ErrSessionBusy
	}
	if !conv.ExecutionID.Valid {
		return ErrNothingToContinue
	}
	bot, err := s.q.GetBot(ctx, botID)
	if err != nil {
		return err
	}
	err = s.engine.Continue(ctx, engine.Ref{BotID: botID, ID: conv.ExecutionID.Int64}, requestID, sessionOptions(bot, conv))
	if err == nil {
		s.signal(sessionID)
	}
	return err
}

// Compact summarizes the older context of a writable session. It also works while the session runs; the
// summary joins the context at the run's next boundary. The transcript shows the progress and the
// outcome. Instructions say what the summary must keep.
func (s *Service) Compact(ctx context.Context, botID string, sessionID int64, instructions, requestID string) error {
	c, err := s.Get(ctx, botID, sessionID)
	if err != nil {
		return err
	}
	if !c.Writable {
		return ErrNotFound
	}
	unlock, err := s.lockSession(ctx, sessionID)
	if err != nil {
		return err
	}
	defer unlock()
	if s.leaseUntil.Load() != 0 {
		return engine.ErrMaintenance
	}
	conv, err := s.q.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if conv.AbortPending != 0 {
		return ErrSessionBusy
	}
	if !conv.ExecutionID.Valid {
		return ErrNothingToCompact
	}
	bot, err := s.q.GetBot(ctx, botID)
	if err != nil {
		return err
	}
	if requestID != "" {
		created, err := s.q.CreateCompactReceipt(ctx, sqlc.CreateCompactReceiptParams{ID: requestID, SessionID: sessionID, Instructions: instructions})
		if err != nil {
			return err
		}
		if created == 0 {
			receipt, err := s.q.GetCompactReceipt(ctx, requestID)
			if err != nil {
				return err
			}
			if receipt.SessionID != sessionID || receipt.Instructions != instructions {
				return ErrOperationID
			}
		}
	}
	err = s.engine.Compact(ctx, engine.Ref{BotID: botID, ID: conv.ExecutionID.Int64}, instructions, requestID, sessionOptions(bot, conv))
	if err == nil {
		s.signal(sessionID)
	}
	return err
}
