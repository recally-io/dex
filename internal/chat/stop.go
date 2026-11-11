package chat

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// Stop withdraws this conversation's input and requests cancellation of its runs. For each child
// session of the conversation it does the same for the input that the parent started and for the run
// that holds it. A child's own pending user input is not withdrawn.
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
	// stored is the result of the first run of a replayed tool call.
	stored string
}

// Withdraw takes the writer before reading. The first commit defines Stop's scope: the conversation
// and the child sessions of it that have pending input.
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
			if prior.AgentID != agentID || prior.ConversationID != call.parent || prior.SourceRequestID != call.task.SourceRequestID || prior.Action != toolStopAction {
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
	now := sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}
	for _, c := range conversations {
		if _, err := q.StopConversationSubmissions(ctx, sqlc.StopConversationSubmissionsParams{ChatID: c.ID, Now: now, ParentOnly: boolInt(c.ID != id)}); err != nil {
			return withdrawal{}, err
		}
		if err := q.RequestExecutionAbort(ctx, c.ID); err != nil {
			return withdrawal{}, err
		}
		if err := q.DismissQuestions(ctx, sqlc.DismissQuestionsParams{ConversationID: c.ID, Now: now}); err != nil {
			return withdrawal{}, err
		}
		if c.ExecutionID.Valid {
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
		if err := q.CreateRoutineToolOperation(ctx, sqlc.CreateRoutineToolOperationParams{Key: call.task.Key, AgentID: agentID, ConversationID: call.parent, SourceRequestID: call.task.SourceRequestID, Action: toolStopAction, Result: string(data)}); err != nil {
			return withdrawal{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return withdrawal{}, err
	}
	return withdrawal{conversations: conversations, targets: targets}, nil
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
