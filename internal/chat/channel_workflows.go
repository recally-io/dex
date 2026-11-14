package chat

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// ChannelConversation resolves controls without selecting a private fallback for a group.
func (s *Service) ChannelConversation(ctx context.Context, agentID, peer string, identity ChannelContext) (Chat, error) {
	allowed, err := s.TelegramInputAllowed(ctx, agentID, peer, identity)
	if err != nil {
		return Chat{}, err
	}
	if !allowed {
		return Chat{}, ErrGroupUnauthorized
	}
	if groupPeer(peer) {
		return s.GroupConversation(ctx, agentID, peer, identity)
	}
	return s.Main(ctx, agentID)
}

// ChannelChildren uses only the current conversation, never the private global session list.
func (s *Service) ChannelChildren(ctx context.Context, agentID, peer string, identity ChannelContext) ([]engine.SessionRef, error) {
	target, err := s.ChannelConversation(ctx, agentID, peer, identity)
	if err != nil {
		return nil, err
	}
	parent, err := s.q.GetConversation(ctx, target.ID)
	if err != nil {
		return nil, err
	}
	children, err := s.q.ListAudienceChildren(ctx, sqlc.ListAudienceChildrenParams{AgentID: parent.AgentID, ParentID: sql.NullInt64{Int64: parent.ID, Valid: true}, ExecutionScope: parent.ExecutionScope, ScopeContext: parent.ScopeContext})
	if err != nil {
		return nil, err
	}
	out := make([]engine.SessionRef, 0, len(children))
	for _, child := range children {
		ref, err := s.ref(ctx, s.q, child)
		if err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, nil
}

// ResumeChannelWorkflow reuses a saved direct child and its immutable initiating parent source.
// It does not run a private Routine or create another work engine.
func (s *Service) ResumeChannelWorkflow(ctx context.Context, agentID, peer, operation, id, message string, identity ChannelContext) error {
	target, err := s.ChannelConversation(ctx, agentID, peer, identity)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkTelegramControl(ctx, q, operation, agentID, target.ID); err != nil {
		return err
	}
	if err := q.LockRoutineTools(ctx); err != nil {
		return err
	}
	allowed, err := TelegramInputAllowed(ctx, q, agentID, peer, identity)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrGroupUnauthorized
	}
	parent, err := q.GetConversation(ctx, target.ID)
	if err != nil {
		return err
	}
	child, err := s.childOf(ctx, q, parent, id)
	if err != nil {
		return err
	}
	first, err := q.GetChildWorkflowSource(ctx, child.ID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(message) == "" {
		message = first.Content
	}
	if err := checkMessage(message); err != nil {
		return err
	}
	original, err := q.GetSubmission(ctx, first.ParentRequestID.String)
	if err != nil {
		return err
	}
	var actor ChannelContext
	if err := json.Unmarshal([]byte(original.ChannelContext), &actor); err != nil {
		return err
	}
	if original.ConversationID != parent.ID || original.Origin != peer || original.ChannelRevoked != 0 || actor.AccountID != identity.AccountID || actor.SenderID != identity.SenderID {
		return fmt.Errorf("%w: only the initiating Telegram actor resumes this saved child", ErrGroupUnauthorized)
	}
	if parent.ExecutionScope != "" {
		if err := authorizeGroupSource(ctx, q, parent, original); err != nil {
			return err
		}
	}
	_, err = q.CreateParentTurn(ctx, sqlc.CreateParentTurnParams{ID: turnID("telegram-workflow:" + operation), ConversationID: child.ID, Content: message, WhenBusy: "followUp", ParentRequestID: sql.NullString{String: original.ID, Valid: true}, CreatedAt: s.now().UnixMilli()})
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.signal(child.ID)
	s.signal(parent.ID)
	s.Wake()
	return nil
}
