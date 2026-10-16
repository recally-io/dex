package chat

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worker"
)

// Stop withdraws this conversation's input and requests cancellation of its runs.
func (s *Service) Stop(ctx context.Context, agentID string, id int64) error {
	return s.stop(ctx, agentID, id, nil)
}

// StopChannelCommand saves the answer in the withdrawal transaction.
// Replay can finish the receipt without withdrawing or aborting later work.
func (s *Service) StopChannelCommand(ctx context.Context, agentID string, id int64, sourceKey, answer string) error {
	return s.stop(ctx, agentID, id, &sqlc.SaveChannelCommandAnswerParams{SourceKey: sourceKey, Answer: answer})
}

func (s *Service) stop(ctx context.Context, agentID string, id int64, command *sqlc.SaveChannelCommandAnswerParams) error {
	if _, err := s.Get(ctx, agentID, id); err != nil {
		return err
	}
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	conversations, targets, call, err := s.withdraw(ctx, agentID, id, command)
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
		s.abort(ctx, call, target)
	}
	s.Wake()
	return nil
}

// Withdraw and spawn take the writer before reading. The first commit defines Stop's scope.
func (s *Service) withdraw(ctx context.Context, agentID string, id int64, command *sqlc.SaveChannelCommandAnswerParams) ([]sqlc.Conversation, []worker.AbortParams, worker.CallFunc, error) {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	tx, q, err := s.taskTransaction(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	conversations, err := q.ListStopConversations(ctx, sqlc.ListStopConversationsParams{AgentID: agentID, ChatID: id})
	if err != nil {
		return nil, nil, nil, err
	}
	var targets []worker.AbortParams
	now := sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}
	for _, c := range conversations {
		if _, err := q.StopConversationSubmissions(ctx, sqlc.StopConversationSubmissionsParams{ChatID: c.ID, Now: now}); err != nil {
			return nil, nil, nil, err
		}
		if c.WorkerConversationID.Valid {
			targets = append(targets, worker.AbortParams{AgentID: agentID, ConversationID: c.WorkerConversationID.Int64})
		}
		tasks, err := q.ListActiveTasks(ctx, c.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, task := range tasks {
			target, err := cancelTaskInput(ctx, q, task, now)
			if err != nil {
				return nil, nil, nil, err
			}
			if target.ConversationID != 0 {
				targets = append(targets, target)
			}
		}
	}
	// Bind before recovery can publish the next worker. Cancellation stays in this process.
	call, _ := s.worker.Bind()
	if command != nil {
		if err := q.SaveChannelCommandAnswer(ctx, *command); err != nil {
			return nil, nil, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, nil, err
	}
	return conversations, targets, call, nil
}

func cancelTaskInput(ctx context.Context, q *sqlc.Queries, task sqlc.Task, now sql.NullInt64) (worker.AbortParams, error) {
	if _, err := q.SettleTask(ctx, sqlc.SettleTaskParams{ID: task.ID, Status: "cancelled", Report: task.Report, EndedAt: now}); err != nil {
		return worker.AbortParams{}, err
	}
	if _, err := q.SettleSubmission(ctx, sqlc.SettleSubmissionParams{ID: task.ID, Status: "unanswered", Reason: sql.NullString{String: "stopped", Valid: true}, SettledAt: now}); err != nil {
		return worker.AbortParams{}, err
	}
	if !task.StartedAt.Valid {
		return worker.AbortParams{}, nil
	}
	c, err := q.GetConversation(ctx, task.ConversationID)
	if err != nil {
		return worker.AbortParams{}, err
	}
	return worker.AbortParams{AgentID: c.AgentID, ConversationID: c.WorkerConversationID.Int64}, nil
}

func (s *Service) abort(ctx context.Context, call worker.CallFunc, target worker.AbortParams) {
	// A held lease already blocks admission and has no unfinished worker work.
	if s.leaseUntil.Load() > s.now().UnixMilli() {
		return
	}
	if call == nil {
		slog.Warn("cancellation saved while worker is unavailable", "conversation", target.ConversationID)
		return
	}
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Second)
	defer cancel()
	if err := call(callCtx, "abort", target, &struct{}{}); err != nil {
		slog.Warn("request conversation cancellation", "conversation", target.ConversationID, "err", err)
	}
}
