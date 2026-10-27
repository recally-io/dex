package chat

import (
	"context"
	"database/sql"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// TaskRecords gives the human all work of this bot. Model tools keep their parent scope.
func (s *Service) TaskRecords(ctx context.Context, agentID string) ([]sqlc.ListAgentTasksRow, error) {
	return s.q.ListAgentTasks(ctx, agentID)
}

func (s *Service) TaskRecord(ctx context.Context, agentID, id string) (sqlc.Task, error) {
	t, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: id, AgentID: agentID})
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrTaskNotFound
	}
	return t, err
}

func (s *Service) TaskWaitReason(ctx context.Context, agentID, id string) (string, error) {
	t, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: id, AgentID: agentID})
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrTaskNotFound
	}
	if err != nil {
		return "", err
	}
	return s.taskWaitReason(ctx, t), nil
}
func (s *Service) TaskExecution(ctx context.Context, agentID, id string) (int64, error) {
	t, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: id, AgentID: agentID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrTaskNotFound
		}
		return 0, err
	}
	c, err := s.q.GetConversation(ctx, t.ConversationID)
	if err != nil {
		return 0, err
	}
	return c.ExecutionID.Int64, nil
}
