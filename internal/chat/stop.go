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

// CancelSession withdraws this conversation's input and requests cancellation of its runs. Each child session
// that holds input that the parent started is stopped as a whole, or not at all:
//
//   - A child without pending user input is stopped like the conversation: its input is withdrawn
//     and its run is aborted.
//   - A child with pending user input, running or queued, keeps its run, because native abort cannot
//     spare queued input. Stop withdraws only the parent input that is still queued. A run that the
//     parent started continues, and its result can start a new parent turn after Stop.
func (s *Service) CancelSession(ctx context.Context, agentID string, id int64) error {
	_, err := s.cancelSession(ctx, agentID, id, nil, nil)
	return err
}

// StopChannelCommand saves the answer in the withdrawal transaction.
// A repeated channel command can finish its receipt without stopping later work.
func (s *Service) StopChannelCommand(ctx context.Context, agentID string, id int64, sourceKey, answer string) error {
	_, err := s.cancelSession(ctx, agentID, id, &sqlc.SaveChannelCommandAnswerParams{SourceKey: sourceKey, Answer: answer}, nil)
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

// cancelSession returns the stored result of a replayed tool call, and an empty string otherwise.
func (s *Service) cancelSession(ctx context.Context, agentID string, id int64, command *sqlc.SaveChannelCommandAnswerParams, call *stopCall) (string, error) {
	if _, err := s.Get(ctx, agentID, id); err != nil {
		return "", err
	}
	unlock, err := s.lockSession(ctx, id)
	if err != nil {
		return "", err
	}
	w, err := s.withdraw(ctx, agentID, id, command, call)
	// Do not take a child's gate until the parent gate is released.
	unlock()
	if err != nil || w.stored != "" {
		return w.stored, err
	}
	for _, c := range w.conversations {
		s.signal(c.ID)
		if s.notify != nil {
			s.notify(c.ChannelKey)
		}
	}
	var errs []error
	for _, c := range w.conversations {
		if c.AgentID != agentID {
			continue // the target's dispatcher owns cross-bot cancellations
		}
		gate := s.sessionGate(c.ID)
		select {
		case gate.token <- struct{}{}:
			callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Second)
			err := s.reconcileStop(callCtx, c.ID)
			cancel()
			if err != nil {
				s.retrySession(ctx, c.ID)
				if !errors.Is(err, errStopPending) {
					errs = append(errs, err)
				}
			}
			<-gate.token
		default:
			// An active target owner will release the gate and wake dispatch.
		}
	}
	s.Wake()
	return "", errors.Join(errs...)
}

type withdrawal struct {
	conversations []sqlc.Conversation
	// stored is the result of the first run of a replayed tool call.
	stored string
}

// Withdraw takes the writer before reading. The first commit defines Stop's scope: the conversation
// and, when included, its child sessions with pending input of the parent. The writer also admits
// user input, so a child is either stopped as a whole or has the user input that this read saw.
func (s *Service) withdraw(ctx context.Context, agentID string, id int64, command *sqlc.SaveChannelCommandAnswerParams, call *stopCall) (withdrawal, error) {
	s.admissionMu.RLock()
	defer s.admissionMu.RUnlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return withdrawal{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if command != nil {
		if err := checkTelegramControl(ctx, q, command.SourceKey, agentID, id); err != nil {
			return withdrawal{}, err
		}
	}
	if call != nil {
		prior, err := q.GetRoutineToolOperation(ctx, call.task.Key)
		if err == nil {
			if prior.AgentID != call.task.AgentID || prior.ConversationID != call.parent || prior.SourceRequestID != call.task.SourceRequestID || prior.Action != toolStopAction {
				return withdrawal{}, fmt.Errorf("%w: tool key belongs to another operation", errSessionInvalid)
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
	now := sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}
	for _, c := range conversations {
		if c.ID != id {
			pending, err := q.ListChildPending(ctx, c.ID)
			if err != nil {
				return withdrawal{}, err
			}
			if slices.ContainsFunc(pending, func(p sqlc.ListChildPendingRow) bool { return !p.FromParent }) {
				if c.ExecutionID.Valid && s.leaseUntil.Load() != 0 {
					return withdrawal{}, engine.ErrMaintenance
				}
				if err := q.RequestParentInputStop(ctx, c.ID); err != nil {
					return withdrawal{}, err
				}
				continue
			}
		}
		if c.AgentID == agentID && c.ChannelKey == MainKey {
			// Session cancellation withdraws the Inbox, so no task Stop needs to resume it.
			if err := q.SupersedeMainTurnStops(ctx, c.ID); err != nil {
				return withdrawal{}, err
			}
			if err := q.DiscardTelegramInputEffects(ctx, agentID); err != nil {
				return withdrawal{}, err
			}
			if err := q.DiscardPrivateTelegramAlbums(ctx, agentID); err != nil {
				return withdrawal{}, err
			}
		}
		if !c.ParentID.Valid {
			if err := q.InsertStoppedOutboxForConversation(ctx, sqlc.InsertStoppedOutboxForConversationParams{AgentID: c.AgentID, ChatID: c.ID, Now: now.Int64}); err != nil {
				return withdrawal{}, err
			}
		}
		if _, err := q.StopConversationSubmissions(ctx, sqlc.StopConversationSubmissionsParams{ChatID: c.ID, Now: now}); err != nil {
			return withdrawal{}, err
		}
		if err := q.RequestExecutionAbort(ctx, c.ID); err != nil {
			return withdrawal{}, err
		}
		if err := q.DismissQuestions(ctx, sqlc.DismissQuestionsParams{ConversationID: c.ID, Now: now}); err != nil {
			return withdrawal{}, err
		}
		if _, err := q.CancelMonitorsByConversation(ctx, sqlc.CancelMonitorsByConversationParams{ConversationID: c.ID, UpdatedAt: now.Int64}); err != nil {
			return withdrawal{}, err
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
	return withdrawal{conversations: conversations}, nil
}

var errStopPending = errors.New("session cancellation is pending")

// reconcileStop owns only the target gate. All native calls run outside the control writer.
func (s *Service) reconcileStop(ctx context.Context, id int64) error {
	c, err := s.q.GetConversation(ctx, id)
	if err != nil {
		return err
	}
	ref := engine.Ref{AgentID: c.AgentID, ID: c.ExecutionID.Int64}
	if c.AbortPending != 0 {
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Second)
		defer cancel()
		s.stopBackground(callCtx, []int64{id})
		if c.ExecutionID.Valid {
			if err := s.engine.StopSession(callCtx, ref); err != nil && !errors.Is(err, engine.ErrNotFound) {
				if errors.Is(err, engine.ErrModelsPending) || errors.Is(err, engine.ErrMaintenance) {
					return errStopPending
				}
				slog.Warn("cancel native execution", "session", id, "native_conversation_id", ref.ID, "reason", admissionRetryReason(err))
				return err
			}
		}
		if err := s.q.FinishExecutionAbort(callCtx, id); err != nil {
			return err
		}
	}
	pending, err := s.q.ListParentInputStops(ctx, id)
	if err != nil {
		return err
	}
	for _, request := range pending {
		outcome := engine.NotAdmitted
		if c.ExecutionID.Valid {
			outcome, err = s.engine.Withdraw(ctx, ref, request)
			if err != nil {
				return err
			}
		}
		// The exact request still belongs to this snapshot. A placed input keeps running.
		if outcome != engine.Placed {
			if _, err := s.q.StopSubmission(ctx, sqlc.StopSubmissionParams{ID: request, Now: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}}); err != nil {
				return err
			}
			s.signal(id)
		}
		if err := s.q.FinishParentInputStop(ctx, request); err != nil {
			return err
		}
	}
	return s.reconcileMainStops(ctx, id, ref)
}
