package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/recally-io/dex/internal/db"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/dream"
	"github.com/recally-io/dex/internal/engine"
)

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrSessionInvalid  = errors.New("invalid session request")
)

// acceptsInput is the one rule for user input, project and model changes. Main and Chat sessions
// always accept it, and so does a child session: a person may write to it while it runs.
func acceptsInput(key string, parent sql.NullInt64) bool {
	return parent.Valid || Writable(key)
}

// Acquire SQLite's writer before reading, so that the checks and the writes of one operation
// cannot interleave with another writer.
func (s *Service) writeTransaction(ctx context.Context) (*sql.Tx, *sqlc.Queries, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE conversations SET id = id WHERE 0"); err != nil {
		return nil, nil, errors.Join(err, tx.Rollback())
	}
	return tx, s.q.WithTx(tx), nil
}

// toolCaller validates the execution owner of a tool call and returns its conversation.
func (s *Service) toolCaller(ctx context.Context, r engine.TaskContext) (sqlc.Conversation, error) {
	conv, err := s.q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: r.AgentID, ExecutionID: sql.NullInt64{Int64: r.ConversationID, Valid: true}})
	if err != nil {
		return conv, err
	}
	source, err := s.q.GetSubmission(ctx, r.SourceRequestID)
	if err != nil {
		return conv, err
	}
	if source.ConversationID != conv.ID {
		return conv, ErrSessionInvalid
	}
	return conv, nil
}

// liveSource returns the input that started the run of the caller, and fails when that run is over.
func liveSource(ctx context.Context, q *sqlc.Queries, r engine.TaskContext) (sqlc.Submission, error) {
	if err := db.RequireActive(ctx, q, r.AgentID); err != nil {
		return sqlc.Submission{}, err
	}
	conv, err := q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: r.AgentID, ExecutionID: sql.NullInt64{Int64: r.ConversationID, Valid: true}})
	if err != nil {
		return sqlc.Submission{}, err
	}
	source, err := q.GetSubmission(ctx, r.SourceRequestID)
	if err != nil {
		return source, err
	}
	if source.ConversationID != conv.ID || source.Status != "pending" {
		return source, fmt.Errorf("%w: the source run is no longer active", ErrSessionInvalid)
	}
	return source, nil
}

// sessionParent returns the conversation of a caller that may manage child sessions. A child and
// a dream cannot, whatever tools they have.
func (s *Service) sessionParent(ctx context.Context, r engine.TaskContext) (sqlc.Conversation, error) {
	conv, err := s.toolCaller(ctx, r)
	if err != nil {
		return conv, err
	}
	if conv.ParentID.Valid || strings.HasPrefix(conv.ChannelKey, dream.Channel) {
		return conv, fmt.Errorf("%w: only a conversation without a parent manages child sessions", ErrSessionInvalid)
	}
	return conv, nil
}
