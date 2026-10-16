package chat

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/worker"
)

const MainKey = "main"
const ChatPrefix = "chat:"

func Writable(key string) bool {
	return key == MainKey || strings.HasPrefix(key, ChatPrefix)
}

// Create reuses an empty Chat without opening a worker context or requiring a model.
func (s *Service) Create(ctx context.Context, agentID string) (Chat, error) {
	c, err := s.q.EnsureEmptyChat(ctx, sqlc.EnsureEmptyChatParams{
		AgentID: agentID, ChannelKey: ChatPrefix + uuid.NewV7().String(), CreatedAt: s.now().UnixMilli(),
	})
	if err != nil {
		return Chat{}, err
	}
	return s.Get(ctx, agentID, c.ID)
}

var ErrConversationBusy = errors.New("the timeline is busy; wait for its replies before clearing context")
var ErrResetID = errors.New("the reset ID belongs to another input")

// Main creates only the control record. The worker conversation opens on first use.
func (s *Service) Main(ctx context.Context, agentID string) (Chat, error) {
	agent, err := s.q.GetAgent(ctx, agentID)
	if err != nil {
		return Chat{}, err
	}
	c, err := s.q.EnsureConversation(ctx, sqlc.EnsureConversationParams{
		AgentID: agentID, ChannelKey: MainKey, Title: agent.Name, CreatedAt: s.now().UnixMilli(),
	})
	if err != nil {
		return Chat{}, err
	}
	return s.Get(ctx, agentID, c.ID)
}

// Clear stores a reset only when both owners are idle. Delivery cannot pass this check.
func (s *Service) Clear(ctx context.Context, agentID string, chatID int64, requestID, origin string) error {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	c, err := s.Get(ctx, agentID, chatID)
	if err != nil {
		return err
	}
	if !Writable(c.ChannelKey) {
		return ErrNotFound
	}
	if sub, err := s.q.GetSubmission(ctx, requestID); err == nil {
		if sub.ConversationID != chatID || sub.Kind != "context_reset" {
			return ErrResetID
		}
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if c.Busy || s.leaseUntil.Load() > s.now().UnixMilli() {
		return ErrConversationBusy
	}
	if c.WorkerConversationID != 0 {
		if err := s.worker.Call(ctx, "checkIdle", worker.AbortParams{AgentID: agentID, ConversationID: c.WorkerConversationID}, &struct{}{}); err != nil {
			if isBusy(err) {
				return ErrConversationBusy
			}
			return err
		}
	}
	// Take the writer before reading. Input and task-result inserts use the same SQLite writer.
	// Do not hold this transaction during RPC: settlement uses the worker's stdout reader.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "UPDATE submissions SET id = id WHERE 0"); err != nil {
		return err
	}
	q := s.q.WithTx(tx)
	pending, err := q.CountPendingChat(ctx, chatID)
	if err != nil {
		return err
	}
	if pending != 0 {
		return ErrConversationBusy
	}
	created, err := q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{
		ID: requestID, ConversationID: chatID, Attachments: "null", Origin: origin, Kind: "context_reset", CreatedAt: s.now().UnixMilli(),
	})
	if err != nil {
		return err
	}
	if created == 0 {
		return ErrResetID
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.signal(chatID)
	s.Wake()
	return nil
}

func inputOrigin(key string) string {
	switch {
	case Writable(key), strings.HasPrefix(key, "web:"):
		return "web"
	case strings.HasPrefix(key, "automation:"):
		return "automation"
	case strings.HasPrefix(key, "task:"):
		return "task"
	case strings.HasPrefix(key, "dream:"):
		return "dream"
	default:
		peer, _, _ := strings.Cut(key, "/")
		return peer
	}
}
