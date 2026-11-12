package chat

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// Stop withdraws this conversation's input and requests cancellation of its runs. Each child session
// that holds input that the parent started is stopped as a whole, or not at all:
//
//   - A child without pending user input is stopped like the conversation: its input is withdrawn
//     and its run is aborted.
//   - A child with pending user input, running or queued, keeps its run, because native abort cannot
//     spare queued input. Stop withdraws only the parent input that is still queued. A run that the
//     parent started continues, and its result can start a new parent turn after Stop.
func (s *Service) Stop(ctx context.Context, agentID string, id int64) error {
	_, err := s.stop(ctx, agentID, id, nil, nil)
	return err
}

// StopChannelCommand saves the answer in the withdrawal transaction.
// A repeated channel command can finish its receipt without stopping later work.
func (s *Service) StopChannelCommand(ctx context.Context, agentID string, id int64, sourceKey, answer string) error {
	_, err := s.stop(ctx, agentID, id, &sqlc.SaveChannelCommandAnswerParams{SourceKey: sourceKey, Answer: answer}, nil)
	return err
}

// stopCall is a session_stop tool call. Its receipt commits with the withdrawals, so a replay
// returns the stored result and cannot stop work that began after the first call.
type stopCall struct {
	task   engine.TaskContext
	parent int64
	child  sqlc.Conversation
}

// toolStopAction names the receipt of a session_stop call in routine_tool_operations, the table of
// replay-safe tool receipts. The table has no routine-only column.
const toolStopAction = "session_stop"

// stop returns the stored result of a replayed tool call, and an empty string otherwise.
func (s *Service) stop(ctx context.Context, agentID string, id int64, command *sqlc.SaveChannelCommandAnswerParams, call *stopCall) (string, error) {
	if _, err := s.Get(ctx, agentID, id); err != nil {
		return "", err
	}
	unlock, err := s.lockAgent(ctx, agentID)
	if err != nil {
		return "", err
	}
	defer func() { unlock(); s.Wake() }()
	w, err := s.withdraw(ctx, agentID, id, command, call)
	if err != nil || w.stored != "" {
		return w.stored, err
	}
	conversations, targets := w.conversations, w.targets
	s.stopBackground(ctx, w.stopped)
	for _, c := range conversations {
		s.signal(c.ID)
		if s.notify != nil {
			s.notify(c.ChannelKey)
		}
	}
	for _, target := range targets {
		if _, err := s.abort(ctx, target); err != nil {
			s.scheduleRetry()
			return "", err
		}
	}
	s.Wake()
	return "", nil
}

type withdrawal struct {
	conversations []sqlc.Conversation
	targets       []engine.Ref
	// stopped are the conversations that the stop ended as a whole. A child that keeps its run is not one.
	stopped []int64
	// stored is the result of the first run of a replayed tool call.
	stored string
}

// Withdraw takes the writer before reading. The first commit defines Stop's scope: the conversation
// and the child sessions of it that have pending input of the parent. The writer also admits user
// input, so a child is either stopped as a whole or has the user input that this read saw.
func (s *Service) withdraw(ctx context.Context, agentID string, id int64, command *sqlc.SaveChannelCommandAnswerParams, call *stopCall) (withdrawal, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return withdrawal{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if call != nil {
		prior, err := q.GetRoutineToolOperation(ctx, call.task.Key)
		if err == nil {
			if prior.AgentID != call.task.AgentID || prior.ConversationID != call.parent || prior.SourceRequestID != call.task.SourceRequestID || prior.Action != toolStopAction {
				return withdrawal{}, fmt.Errorf("%w: tool key belongs to another operation", ErrSessionInvalid)
			}
			return withdrawal{stored: prior.Result}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return withdrawal{}, err
		}
	}
	conversations, err := q.ListStopConversations(ctx, sqlc.ListStopConversationsParams{AgentID: agentID, ChatID: id})
	if err != nil {
		return withdrawal{}, err
	}
	var targets []engine.Ref
	var stopped []int64
	now := sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}
	for _, c := range conversations {
		if c.ID != id {
			pending, err := q.ListChildPending(ctx, c.ID)
			if err != nil {
				return withdrawal{}, err
			}
			if slices.ContainsFunc(pending, func(p sqlc.ListChildPendingRow) bool { return !p.FromParent }) {
				if err := s.withdrawQueuedParentInputs(ctx, q, c, pending, now); err != nil {
					return withdrawal{}, err
				}
				continue
			}
		}
		if _, err := q.StopConversationSubmissions(ctx, sqlc.StopConversationSubmissionsParams{ChatID: c.ID, Now: now}); err != nil {
			return withdrawal{}, err
		}
		stopped = append(stopped, c.ID)
		if err := q.RequestExecutionAbort(ctx, c.ID); err != nil {
			return withdrawal{}, err
		}
		if err := q.DismissQuestions(ctx, sqlc.DismissQuestionsParams{ConversationID: c.ID, Now: now}); err != nil {
			return withdrawal{}, err
		}
		// A child of another bot is not aborted here. Its bot has its own delivery gate, and this call
		// holds one gate only. The abort request above is durable, and the dispatcher of that bot
		// carries it out. Holding two gates could deadlock two parents that stop each other's children.
		if c.ExecutionID.Valid && c.AgentID == agentID {
			targets = append(targets, engine.Ref{AgentID: agentID, ID: c.ExecutionID.Int64})
		}
	}
	if command != nil {
		if err := q.SaveChannelCommandAnswer(ctx, *command); err != nil {
			return withdrawal{}, err
		}
	}
	if call != nil {
		ref, err := s.ref(ctx, q, call.child)
		if err != nil {
			return withdrawal{}, err
		}
		data, err := json.Marshal(ref)
		if err != nil {
			return withdrawal{}, err
		}
		if err := q.CreateRoutineToolOperation(ctx, sqlc.CreateRoutineToolOperationParams{Key: call.task.Key, AgentID: call.task.AgentID, ConversationID: call.parent, SourceRequestID: call.task.SourceRequestID, Action: toolStopAction, Result: string(data)}); err != nil {
			return withdrawal{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return withdrawal{}, err
	}
	return withdrawal{conversations: conversations, targets: targets, stopped: stopped}, nil
}

// withdrawQueuedParentInputs withdraws the parent input of a child that native execution still
// holds in its queue. A placed input is part of a run and keeps running. A failed withdrawal fails
// Stop, so the user can repeat it. A pending input that native execution never saw has no run, and
// its receipt settles at once. The native queue belongs to the bot of the child, which can differ
// from the bot of the parent.
func (s *Service) withdrawQueuedParentInputs(ctx context.Context, q *sqlc.Queries, c sqlc.Conversation, pending []sqlc.ListChildPendingRow, now sql.NullInt64) error {
	for _, p := range pending {
		if !p.FromParent {
			continue
		}
		if c.ExecutionID.Valid {
			outcome, err := s.engine.Withdraw(ctx, engine.Ref{AgentID: c.AgentID, ID: c.ExecutionID.Int64}, p.ID)
			if err != nil {
				return err
			}
			if outcome == engine.Placed {
				continue
			}
		}
		if _, err := q.StopSubmission(ctx, sqlc.StopSubmissionParams{ID: p.ID, Now: now}); err != nil {
			return err
		}
	}
	return nil
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
