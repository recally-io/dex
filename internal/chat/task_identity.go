package chat

import (
	"context"
	"database/sql"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// acceptsInput is the one rule for user input, project and model changes. Main and Chat sessions
// always accept it. A delegated session accepts it only after its task ended, so a person never
// competes with the running task. Task status decides, not the channel key.
func acceptsInput(key string, taskStatus sql.NullString) bool {
	if taskStatus.Valid {
		return !occupied(taskStatus.String)
	}
	return Writable(key)
}

// writable applies acceptsInput to a stored conversation.
func (s *Service) writable(ctx context.Context, conv sqlc.Conversation) (bool, error) {
	task, err := s.q.GetTaskByConversation(ctx, conv.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return acceptsInput(conv.ChannelKey, sql.NullString{}), nil
	}
	if err != nil {
		return false, err
	}
	return acceptsInput(conv.ChannelKey, sql.NullString{String: task.Status, Valid: true}), nil
}

// taskRole classifies submission p in a task conversation. The delegated input is the task's own
// input. It carries the task tools, the report_task duty, and task settlement. Any other input in the
// conversation, such as a user message after the task ended, is a continuation: a normal chat input.
func (s *Service) taskRole(ctx context.Context, p sqlc.ListPendingSubmissionsRow) (delegated, continuation bool, err error) {
	task, err := s.q.GetTaskByConversation(ctx, p.ConversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return task.ID == p.ID, task.ID != p.ID, nil
}
