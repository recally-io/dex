package chat

import (
	"context"
	"database/sql"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// TaskConversation returns a hidden stage chat only within its visible parent and task.
func (s *Service) TaskConversation(ctx context.Context, agentID string, parentID int64, taskID, stageID string) (Chat, error) {
	if _, err := s.Get(ctx, agentID, parentID); err != nil {
		return Chat{}, err
	}
	task, err := s.q.GetUserTask(ctx, sqlc.GetUserTaskParams{AgentID: agentID, ParentID: parentID, TaskID: taskID})
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, ErrTaskNotFound
	}
	if err != nil {
		return Chat{}, err
	}
	stage, err := s.q.GetTaskStage(ctx, stageID)
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, ErrTaskNotFound
	}
	if err != nil {
		return Chat{}, err
	}
	if stage.UserTaskID != task.ID {
		return Chat{}, ErrTaskNotFound
	}
	c, err := s.q.GetConversation(ctx, stage.ConversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, ErrTaskNotFound
	}
	if err != nil {
		return Chat{}, err
	}
	if c.AgentID != agentID {
		return Chat{}, ErrTaskNotFound
	}
	return Chat{ID: c.ID, ChannelKey: c.ChannelKey, Title: c.Title, CreatedAt: c.CreatedAt,
		Settings:             Settings{Provider: c.ModelProvider, ModelID: c.ModelID, ThinkingLevel: c.ThinkingLevel},
		WorkerConversationID: c.WorkerConversationID.Int64}, nil
}
