package chat

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// ToolFork scopes the request to the calling conversation and its durable tool key.
func (s *Service) ToolFork(ctx context.Context, source engine.TaskContext, entryID int64) (int64, error) {
	conv, err := s.taskCaller(ctx, source)
	if err != nil {
		return 0, err
	}
	fork, err := s.Fork(ctx, source.AgentID, conv.ID, entryID, source.Key)
	if err != nil {
		return 0, err
	}
	return fork.ID, nil
}

// Fork creates a Chat with the source's effective settings and the selected committed history.
// A saved operation is reconciled with the same native operation ID after a restart.
func (s *Service) Fork(ctx context.Context, agentID string, sourceID, entryID int64, operationID string) (Chat, error) {
	if operationID == "" || entryID <= 0 {
		return Chat{}, engine.ErrInvalidInput
	}
	unlock, err := s.lockAgent(ctx, agentID)
	if err != nil {
		return Chat{}, err
	}
	defer func() { unlock(); s.Wake() }()
	source, err := s.Get(ctx, agentID, sourceID)
	if err != nil {
		return Chat{}, err
	}
	if source.ExecutionID == 0 {
		return Chat{}, ErrNotFound
	}
	if s.leaseUntil.Load() != 0 {
		return Chat{}, engine.ErrMaintenance
	}
	s.admissionMu.RLock()
	op, err := s.createForkReceipt(ctx, agentID, source, entryID, operationID)
	s.admissionMu.RUnlock()
	if err != nil {
		return Chat{}, err
	}
	if op.Status == "rejected" {
		return Chat{}, ErrNotFound
	}
	if op.Status == "pending" {
		if err = s.completeFork(ctx, op); err != nil {
			return Chat{}, err
		}
	}
	return s.Get(ctx, agentID, op.TargetConversationID)
}

func (s *Service) createForkReceipt(ctx context.Context, agentID string, source Chat, entryID int64, operationID string) (sqlc.ForkOperation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return sqlc.ForkOperation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "UPDATE fork_operations SET operation_id = operation_id WHERE 0"); err != nil {
		return sqlc.ForkOperation{}, err
	}
	q := s.q.WithTx(tx)
	prior, err := q.GetForkOperation(ctx, operationID)
	if err == nil {
		if prior.AgentID != agentID || prior.SourceConversationID != source.ID || prior.EntryID != entryID {
			return sqlc.ForkOperation{}, engine.ErrOperationConflict
		}
		return prior, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return sqlc.ForkOperation{}, err
	}
	if s.leaseUntil.Load() != 0 {
		return sqlc.ForkOperation{}, engine.ErrMaintenance
	}
	row, err := q.GetConversation(ctx, source.ID)
	if err != nil {
		return sqlc.ForkOperation{}, err
	}
	agent, err := q.GetAgent(ctx, agentID)
	if err != nil {
		return sqlc.ForkOperation{}, err
	}
	options := engine.Options{Cwd: row.Cwd, Model: engine.ModelRef{Provider: agent.ModelProvider, ModelID: agent.ModelID}, ThinkingLevel: cmp.Or(row.ThinkingLevel, agent.ThinkingLevel), Instructions: agent.Instructions}
	if row.ModelID != "" {
		options.Model = engine.ModelRef{Provider: row.ModelProvider, ModelID: row.ModelID}
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		return sqlc.ForkOperation{}, err
	}
	now := s.now().UnixMilli()
	target, err := q.EnsureConversation(ctx, sqlc.EnsureConversationParams{AgentID: agentID, ChannelKey: ChatPrefix + uuid.NewV7().String(), Title: row.Title, ModelProvider: options.Model.Provider, ModelID: options.Model.ModelID, ThinkingLevel: options.ThinkingLevel, CreatedAt: now})
	if err != nil {
		return sqlc.ForkOperation{}, err
	}
	if err = q.SetConversationCwd(ctx, sqlc.SetConversationCwdParams{ID: target.ID, AgentID: agentID, Cwd: options.Cwd}); err != nil {
		return sqlc.ForkOperation{}, err
	}
	if err = q.CreateForkOperation(ctx, sqlc.CreateForkOperationParams{OperationID: operationID, AgentID: agentID, SourceConversationID: source.ID, TargetConversationID: target.ID, EntryID: entryID, OptionsJson: string(encoded), CreatedAt: now}); err != nil {
		return sqlc.ForkOperation{}, err
	}
	op, err := q.GetForkOperation(ctx, operationID)
	if err != nil {
		return sqlc.ForkOperation{}, err
	}
	if err = tx.Commit(); err != nil {
		return sqlc.ForkOperation{}, err
	}
	return op, nil
}

func (s *Service) completeFork(ctx context.Context, op sqlc.ForkOperation) error {
	source, err := s.q.GetConversation(ctx, op.SourceConversationID)
	if err != nil {
		return err
	}
	var options engine.Options
	if err = json.Unmarshal([]byte(op.OptionsJson), &options); err != nil {
		return fmt.Errorf("decode fork options: %w", err)
	}
	target, err := s.engine.Fork(ctx, engine.Ref{AgentID: op.AgentID, ID: source.ExecutionID.Int64}, op.OperationID, op.EntryID, options)
	if err != nil {
		if errors.Is(err, engine.ErrNotFound) || errors.Is(err, engine.ErrInvalidInput) || errors.Is(err, engine.ErrOperationConflict) {
			if updateErr := s.q.SetForkOperationStatus(ctx, sqlc.SetForkOperationStatusParams{OperationID: op.OperationID, Status: "rejected"}); updateErr != nil {
				return errors.Join(err, updateErr)
			}
		}
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	if err = q.SetExecution(ctx, sqlc.SetExecutionParams{ID: op.TargetConversationID, ExecutionID: sql.NullInt64{Int64: target.ID, Valid: true}}); err != nil {
		return err
	}
	if err = q.SetForkOperationStatus(ctx, sqlc.SetForkOperationStatusParams{OperationID: op.OperationID, Status: "done"}); err != nil {
		return err
	}
	return tx.Commit()
}
