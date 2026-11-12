package chat

import (
	"context"
	"database/sql"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// The tasks table is frozen (ADR 0015). The web UI of the previous release still reads it until
// the cleanup removes the table. Nothing writes it, so a row only describes the task as it was.

var (
	ErrTaskNotFound = errors.New("task not found")
	ErrTaskConflict = errors.New("task state conflicts with this request")
)

// TaskRecords gives the human all legacy task rows of this bot.
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

// AcknowledgeTask records that a terminal task has been handled, without changing its result.
func (s *Service) AcknowledgeTask(ctx context.Context, agentID, id string) (sqlc.Task, error) {
	row, err := s.TaskRecord(ctx, agentID, id)
	if err != nil {
		return row, err
	}
	n, err := s.q.AcknowledgeTask(ctx, sqlc.AcknowledgeTaskParams{TaskID: id, AgentID: agentID, HandledAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}})
	if err != nil {
		return row, err
	}
	if n == 0 {
		return row, ErrTaskConflict
	}
	return s.TaskRecord(ctx, agentID, id)
}

// RestoreTask undoes handling without changing the task result.
func (s *Service) RestoreTask(ctx context.Context, agentID, id string) (sqlc.Task, error) {
	row, err := s.TaskRecord(ctx, agentID, id)
	if err != nil {
		return row, err
	}
	n, err := s.q.RestoreTask(ctx, sqlc.RestoreTaskParams{TaskID: id, AgentID: agentID})
	if err != nil {
		return row, err
	}
	if n == 0 {
		return row, ErrTaskConflict
	}
	return s.TaskRecord(ctx, agentID, id)
}

func (s *Service) TaskExecution(ctx context.Context, agentID, id string) (int64, error) {
	t, err := s.TaskRecord(ctx, agentID, id)
	if err != nil {
		return 0, err
	}
	c, err := s.q.GetConversation(ctx, t.ConversationID)
	if err != nil {
		return 0, err
	}
	return c.ExecutionID.Int64, nil
}
