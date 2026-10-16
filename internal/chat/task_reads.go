package chat

import (
	"context"
	"database/sql"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// TaskRecords gives the human all work of this bot. Model tools keep their parent scope.
func (s *Service) TaskRecords(ctx context.Context, agentID string) ([]sqlc.Task, error) {
	return s.q.ListAgentTasks(ctx, agentID)
}

func (s *Service) TaskRecord(ctx context.Context, agentID, id string) (sqlc.Task, error) {
	t, err := s.q.GetAgentTask(ctx, sqlc.GetAgentTaskParams{ID: id, AgentID: agentID})
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrTaskNotFound
	}
	return t, err
}
