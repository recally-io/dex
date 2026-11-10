package chat

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// Compaction is an operation receipt and its native progress.
type Compaction struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

func (s *Service) Compact(ctx context.Context, agentID string, chatID int64, operationID string) (Compaction, error) {
	return s.compact(ctx, agentID, chatID, operationID, nil)
}

func (s *Service) compact(ctx context.Context, agentID string, chatID int64, operationID string, caller *engine.TaskContext) (Compaction, error) {
	if operationID == "" {
		return Compaction{}, engine.ErrInvalidInput
	}
	unlock, err := s.lockAgent(ctx, agentID)
	if err != nil {
		return Compaction{}, err
	}
	defer func() { unlock(); s.Wake() }()
	chat, err := s.Get(ctx, agentID, chatID)
	if err != nil {
		return Compaction{}, err
	}
	if !chat.Writable {
		return Compaction{}, ErrNotFound
	}
	if caller != nil {
		_, err := s.q.GetCompactOperation(ctx, operationID)
		if errors.Is(err, sql.ErrNoRows) {
			if err := s.requireActiveToolSource(ctx, *caller, chatID); err != nil {
				return Compaction{}, err
			}
		} else if err != nil {
			return Compaction{}, err
		}
	}
	if err := s.finishAgentAborts(ctx, agentID); err != nil {
		return Compaction{}, err
	}
	toolSource := ""
	if caller != nil {
		toolSource = caller.SourceRequestID
	}
	s.admissionMu.RLock()
	op, err := s.createCompactReceipt(ctx, agentID, chat, operationID, toolSource)
	s.admissionMu.RUnlock()
	if err != nil {
		return Compaction{}, err
	}
	if op.Status == "pending" {
		if err = s.admitCompact(ctx, op); err != nil {
			return Compaction{}, err
		}
	}
	return s.compactionStatus(ctx, agentID, chatID, operationID)
}

func (s *Service) createCompactReceipt(ctx context.Context, agentID string, chat Chat, operationID, toolSource string) (sqlc.CompactOperation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return sqlc.CompactOperation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "UPDATE compact_operations SET operation_id = operation_id WHERE 0"); err != nil {
		return sqlc.CompactOperation{}, err
	}
	q := s.q.WithTx(tx)
	prior, err := q.GetCompactOperation(ctx, operationID)
	if err == nil {
		if prior.AgentID != agentID || prior.ConversationID != chat.ID || prior.ToolSourceRequestID != toolSource {
			return sqlc.CompactOperation{}, engine.ErrOperationConflict
		}
		return prior, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return sqlc.CompactOperation{}, err
	}
	if s.leaseUntil.Load() != 0 {
		return sqlc.CompactOperation{}, engine.ErrMaintenance
	}
	state := "pending"
	if chat.ExecutionID == 0 {
		state = "empty"
	}
	if err = q.CreateCompactOperation(ctx, sqlc.CreateCompactOperationParams{OperationID: operationID, AgentID: agentID, ConversationID: chat.ID, Status: state, CreatedAt: s.now().UnixMilli(), ToolSourceRequestID: toolSource}); err != nil {
		return sqlc.CompactOperation{}, err
	}
	op, err := q.GetCompactOperation(ctx, operationID)
	if err != nil {
		return sqlc.CompactOperation{}, err
	}
	if err = tx.Commit(); err != nil {
		return sqlc.CompactOperation{}, err
	}
	return op, nil
}
func (s *Service) admitCompact(ctx context.Context, op sqlc.CompactOperation) error {
	c, err := s.q.GetConversation(ctx, op.ConversationID)
	if err != nil {
		return err
	}
	ref := engine.Ref{AgentID: op.AgentID, ID: c.ExecutionID.Int64}
	var id int64
	if op.ToolSourceRequestID != "" {
		active, readErr := s.toolOperationSourceActive(ctx, op.ToolSourceRequestID, op.ConversationID)
		if readErr != nil {
			return readErr
		}
		if !active {
			var found bool
			id, found, err = s.engine.LookupCompact(ctx, ref, op.OperationID)
			if err != nil {
				return err
			}
			if !found {
				return s.q.RejectCompactOperation(ctx, op.OperationID)
			}
		} else {
			id, err = s.engine.Compact(ctx, ref, op.OperationID)
		}
	} else {
		id, err = s.engine.Compact(ctx, ref, op.OperationID)
	}
	if err != nil {
		return err
	}
	return s.q.AdmitCompactOperation(ctx, sqlc.AdmitCompactOperationParams{OperationID: op.OperationID, TaskID: sql.NullInt64{Int64: id, Valid: true}})
}
func (s *Service) CompactionStatus(ctx context.Context, agentID string, chatID int64, operationID string) (Compaction, error) {
	unlock, err := s.lockAgent(ctx, agentID)
	if err != nil {
		return Compaction{}, err
	}
	defer unlock()
	if _, err := s.Get(ctx, agentID, chatID); err != nil {
		return Compaction{}, err
	}
	return s.compactionStatus(ctx, agentID, chatID, operationID)
}
func (s *Service) compactionStatus(ctx context.Context, agentID string, chatID int64, operationID string) (Compaction, error) {
	op, err := s.q.GetCompactOperation(ctx, operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return Compaction{}, ErrNotFound
	}
	if err != nil {
		return Compaction{}, err
	}
	if op.AgentID != agentID || op.ConversationID != chatID {
		return Compaction{}, ErrNotFound
	}
	out := Compaction{ID: op.OperationID, Status: "pending"}
	if op.Status == "empty" {
		out.Status = "no_op"
		return out, nil
	}
	if op.Status == "rejected" {
		out.Status = "failed"
		out.Reason = "source_closed"
		return out, nil
	}
	if op.Status == "pending" {
		return out, nil
	}
	c, err := s.q.GetConversation(ctx, chatID)
	if err != nil {
		return Compaction{}, err
	}
	native, err := s.engine.CompactionStatus(ctx, engine.Ref{AgentID: agentID, ID: c.ExecutionID.Int64}, op.TaskID.Int64)
	if err != nil {
		return Compaction{}, err
	}
	out.Status = native.Status
	out.Reason = native.Reason
	return out, nil
}
func (s *Service) ToolCompact(ctx context.Context, source engine.TaskContext) (Compaction, error) {
	c, err := s.taskCaller(ctx, source)
	if err != nil {
		return Compaction{}, err
	}
	return s.compact(ctx, source.AgentID, c.ID, strings.ReplaceAll(source.Key, "/", ":"), &source)
}
