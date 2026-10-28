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

func (s *Service) TaskWaitReasons(ctx context.Context, agentID string) (map[string]string, error) {
	rows, err := s.q.ListAgentTasks(ctx, agentID)
	if err != nil {
		return nil, err
	}
	admissions, err := s.q.ListTaskAdmissions(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		task, err := s.q.GetTask(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		conv, err := s.q.GetConversation(ctx, task.ConversationID)
		if err != nil {
			return nil, err
		}
		out[row.ID] = taskWaitReasonFrom(admissions, task, conv)
	}
	return out, nil
}
func (s *Service) TaskWaitReason(ctx context.Context, agentID, id string) (string, error) {
	t, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: id, AgentID: agentID})
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrTaskNotFound
	}
	if err != nil {
		return "", err
	}
	waitReason, err := s.taskWaitReason(ctx, t)
	if err != nil {
		return "unknown", nil //nolint:nilerr // A read failure must not expose scheduler internals to views.
	}
	return waitReason, nil
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
