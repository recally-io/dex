package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// Origin is the chat that a fork copied its history from.
type Origin struct {
	ID    int64
	Title string
	Main  bool
}

// Fork starts a new Chat from the history of a writable chat, up to entryID or, with before, up
// to the entry before it. The fork takes the source's model, thinking level and project, and it
// does not run. A running source contributes only its committed entries. When nothing comes
// before entryID, the fork is an empty Chat with the same settings. Files on disk stay as they are.
func (s *Service) Fork(ctx context.Context, agentID string, chatID, entryID int64, before bool, requestID string) (Chat, error) {
	c, err := s.Get(ctx, agentID, chatID)
	if err != nil {
		return Chat{}, err
	}
	if !c.Writable {
		return Chat{}, ErrNotFound
	}
	if requestID != "" {
		if receipt, err := s.q.GetForkReceipt(ctx, requestID); err == nil {
			return s.forkReplay(ctx, agentID, receipt, chatID, entryID, before)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Chat{}, err
		}
	}
	conv, err := s.q.GetConversation(ctx, chatID)
	if err != nil {
		return Chat{}, err
	}
	var store func(context.Context, *sqlc.Queries, sqlc.Conversation) error
	if requestID != "" {
		store = func(ctx context.Context, q *sqlc.Queries, fork sqlc.Conversation) error {
			created, err := q.CreateForkReceipt(ctx, sqlc.CreateForkReceiptParams{ID: requestID, SourceID: chatID, EntryID: entryID, BeforeEntry: boolInt(before), ForkID: fork.ID})
			if err == nil && created == 0 {
				return errRepeatedFork
			}
			return err
		}
	}
	fork, err := s.fork(ctx, conv, entryID, before, store)
	if errors.Is(err, errRepeatedFork) {
		receipt, err := s.q.GetForkReceipt(ctx, requestID)
		if err != nil {
			return Chat{}, err
		}
		return s.forkReplay(ctx, agentID, receipt, chatID, entryID, before)
	}
	if err != nil {
		return Chat{}, err
	}
	return s.Get(ctx, agentID, fork.ID)
}

// ErrOperationID means an operation ID was reused for different parameters.
var ErrOperationID = errors.New("the operation ID belongs to another request")

var errRepeatedFork = errors.New("fork receipt already exists")

func (s *Service) forkReplay(ctx context.Context, agentID string, receipt sqlc.ForkReceipt, chatID, entryID int64, before bool) (Chat, error) {
	if receipt.SourceID != chatID || receipt.EntryID != entryID || receipt.BeforeEntry != boolInt(before) {
		return Chat{}, ErrOperationID
	}
	return s.Get(ctx, agentID, receipt.ForkID)
}

// fork copies the history of conv into a new Chat that has the parent of conv. The optional store
// runs in the transaction that creates the fork. Entry zero forks at the newest finished answer.
func (s *Service) fork(ctx context.Context, conv sqlc.Conversation, entryID int64, before bool, store func(context.Context, *sqlc.Queries, sqlc.Conversation) error) (sqlc.Conversation, error) {
	if s.leaseUntil.Load() != 0 {
		return sqlc.Conversation{}, engine.ErrMaintenance
	}
	if !conv.ExecutionID.Valid {
		return sqlc.Conversation{}, fmt.Errorf("%w: entry %d is not in this chat", engine.ErrInvalidInput, entryID)
	}
	ref, copied, err := s.engine.Fork(ctx, engine.Ref{AgentID: conv.AgentID, ID: conv.ExecutionID.Int64}, entryID, before)
	if err != nil {
		return sqlc.Conversation{}, err
	}
	title := conv.Title
	if conv.ChannelKey == MainKey {
		title = "Main"
	}
	if title != "" {
		title += " (fork)"
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return sqlc.Conversation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	fork, err := q.CreateFork(ctx, sqlc.CreateForkParams{
		AgentID: conv.AgentID, ChannelKey: ChatPrefix + uuid.NewV7().String(), Title: title,
		ModelProvider: conv.ModelProvider, ModelID: conv.ModelID, ThinkingLevel: conv.ThinkingLevel, Cwd: conv.Cwd,
		ExecutionID: sql.NullInt64{Int64: ref.ID, Valid: copied}, ForkedFrom: sql.NullInt64{Int64: conv.ID, Valid: true}, ParentID: conv.ParentID,
		CreatedAt: s.now().UnixMilli(),
	})
	if err != nil {
		return fork, err
	}
	if store != nil {
		if err := store(ctx, q, fork); err != nil {
			return fork, err
		}
	}
	return fork, tx.Commit()
}
