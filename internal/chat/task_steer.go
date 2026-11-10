package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// ReceiveTaskSteer saves an intent, not a native admission. Until admission is
// implemented, pending receipts are never sent to PiG and remain pending.
func (s *Service) ReceiveTaskSteer(ctx context.Context, caller engine.TaskContext, taskID, requestID, content string) (int64, error) {
	if strings.TrimSpace(requestID) == "" || strings.TrimSpace(content) == "" || len(content) > 128000 {
		return 0, fmt.Errorf("%w: request ID and content are required", ErrTaskInvalid)
	}
	parent, err := s.taskCaller(ctx, caller)
	if err != nil {
		return 0, err
	}
	tx, q, err := s.taskTransaction(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	task, err := ownedTask(ctx, q, caller.AgentID, parent.ID, taskID)
	if err != nil {
		return 0, err
	}
	prior, err := q.GetTaskSteerReceipt(ctx, sqlc.GetTaskSteerReceiptParams{AgentID: caller.AgentID, RequestID: requestID})
	if err == nil {
		if prior.ParentConversationID != parent.ID || prior.TaskID != taskID || prior.Content != content {
			return 0, ErrTaskConflict
		}
		return prior.Sequence, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if _, err := taskSource(ctx, q, caller); err != nil {
		return 0, err
	}
	if !occupied(task.Status) {
		return 0, ErrTaskConflict
	}
	receipt, err := q.CreateTaskSteerReceipt(ctx, sqlc.CreateTaskSteerReceiptParams{
		AgentID: caller.AgentID, ParentConversationID: parent.ID, TaskID: taskID,
		RequestID: requestID, NativeRequestID: "task-steer:" + uuid.NewV7().String(),
		Content: content, CreatedAt: s.now().UnixMilli(),
	})
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return receipt.Sequence, nil
}

// TaskSteerReceipt is scoped to the task's owning parent conversation.
func (s *Service) TaskSteerReceipt(ctx context.Context, caller engine.TaskContext, requestID string) (string, string, error) {
	parent, err := s.taskCaller(ctx, caller)
	if err != nil {
		return "", "", err
	}
	receipt, err := s.q.GetTaskSteerReceipt(ctx, sqlc.GetTaskSteerReceiptParams{AgentID: caller.AgentID, RequestID: requestID})
	if errors.Is(err, sql.ErrNoRows) || (err == nil && receipt.ParentConversationID != parent.ID) {
		return "", "", ErrTaskNotFound
	}
	if err != nil {
		return "", "", err
	}
	return receipt.State, receipt.Reason, nil
}
