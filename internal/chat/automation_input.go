package chat

import (
	"context"
	"errors"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// AutomationInput binds one run record to its durable message intent.
type AutomationInput struct {
	Run                                      sqlc.CreateAutomationRunParams
	AgentID, ChannelKey, Title, Content, Cwd string
}

// EnqueueAutomation commits both records before it wakes native delivery.
func (s *Service) EnqueueAutomation(ctx context.Context, in AutomationInput) (int64, bool, error) {
	s.admissionMu.RLock()
	defer s.admissionMu.RUnlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	created, err := q.CreateAutomationRun(ctx, in.Run)
	if err != nil || created == 0 {
		return 0, false, err
	}
	conv, err := q.EnsureConversation(ctx, sqlc.EnsureConversationParams{AgentID: in.AgentID, ChannelKey: in.ChannelKey, Title: in.Title, CreatedAt: in.Run.CreatedAt})
	if err != nil {
		return 0, false, err
	}
	if err = q.SetConversationCwd(ctx, sqlc.SetConversationCwdParams{ID: conv.ID, AgentID: in.AgentID, Cwd: in.Cwd}); err != nil {
		return 0, false, err
	}
	inserted, err := q.CreateSubmission(ctx, sqlc.CreateSubmissionParams{ID: in.Run.ID, ConversationID: conv.ID, Content: in.Content, Attachments: "[]", Origin: "automation", Kind: "message", CreatedAt: in.Run.CreatedAt})
	if err != nil {
		return 0, false, err
	}
	if inserted != 1 {
		return 0, false, errors.New("automation run ID already has an input intent")
	}
	if err = tx.Commit(); err != nil {
		return 0, false, err
	}
	s.signal(conv.ID)
	s.Wake()
	return conv.ID, true, nil
}
