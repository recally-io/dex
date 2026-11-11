package chat

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// Stop withdraws this conversation's input and requests cancellation of its runs. It does the same
// for each child session of the conversation that has pending input.
func (s *Service) Stop(ctx context.Context, agentID string, id int64) error {
	return s.stop(ctx, agentID, id, nil)
}

// StopChannelCommand saves the answer in the withdrawal transaction.
// A repeated channel command can finish its receipt without stopping later work.
func (s *Service) StopChannelCommand(ctx context.Context, agentID string, id int64, sourceKey, answer string) error {
	return s.stop(ctx, agentID, id, &sqlc.SaveChannelCommandAnswerParams{SourceKey: sourceKey, Answer: answer})
}

func (s *Service) stop(ctx context.Context, agentID string, id int64, command *sqlc.SaveChannelCommandAnswerParams) error {
	if _, err := s.Get(ctx, agentID, id); err != nil {
		return err
	}
	unlock, err := s.lockAgent(ctx, agentID)
	if err != nil {
		return err
	}
	defer func() { unlock(); s.Wake() }()
	conversations, targets, err := s.withdraw(ctx, agentID, id, command)
	if err != nil {
		return err
	}
	for _, c := range conversations {
		s.signal(c.ID)
		if s.notify != nil {
			s.notify(c.ChannelKey)
		}
	}
	for _, target := range targets {
		if _, err := s.abort(ctx, target); err != nil {
			s.scheduleRetry()
			return err
		}
	}
	s.Wake()
	return nil
}

// Withdraw takes the writer before reading. The first commit defines Stop's scope: the conversation
// and the child sessions of it that have pending input.
func (s *Service) withdraw(ctx context.Context, agentID string, id int64, command *sqlc.SaveChannelCommandAnswerParams) ([]sqlc.Conversation, []engine.Ref, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	conversations, err := q.ListStopConversations(ctx, sqlc.ListStopConversationsParams{AgentID: agentID, ChatID: id})
	if err != nil {
		return nil, nil, err
	}
	var targets []engine.Ref
	now := sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}
	for _, c := range conversations {
		if _, err := q.StopConversationSubmissions(ctx, sqlc.StopConversationSubmissionsParams{ChatID: c.ID, Now: now}); err != nil {
			return nil, nil, err
		}
		if err := q.RequestExecutionAbort(ctx, c.ID); err != nil {
			return nil, nil, err
		}
		if err := q.DismissQuestions(ctx, sqlc.DismissQuestionsParams{ConversationID: c.ID, Now: now}); err != nil {
			return nil, nil, err
		}
		if c.ExecutionID.Valid {
			targets = append(targets, engine.Ref{AgentID: agentID, ID: c.ExecutionID.Int64})
		}
	}
	if command != nil {
		if err := q.SaveChannelCommandAnswer(ctx, *command); err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return conversations, targets, nil
}

// abort completes only after native cancellation and its durable acknowledgement.
func (s *Service) abort(ctx context.Context, target engine.Ref) (completed bool, err error) {
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Second)
	defer cancel()
	if err := s.engine.Abort(callCtx, target); err != nil && !errors.Is(err, engine.ErrNotFound) {
		if errors.Is(err, engine.ErrModelsPending) {
			// Discovery completion or retry wakes cancellation before later admission.
			s.scheduleRetry()
			return false, nil
		}
		if errors.Is(err, engine.ErrMaintenance) {
			// Lease publication can follow Quiesce. Keep cancellation durable in both windows.
			s.scheduleRetry()
			return false, nil
		}
		slog.Warn("cancel native execution", "execution", target.ID, "err", err)
		return false, err
	}
	err = s.q.FinishExecutionAbort(callCtx, sqlc.FinishExecutionAbortParams{AgentID: target.AgentID, ExecutionID: sql.NullInt64{Int64: target.ID, Valid: true}})
	return err == nil, err
}
