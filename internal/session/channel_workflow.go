package session

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// DirectChildren lists the direct children of an already resolved channel target, never the
// private global session list.
func (s *Service) DirectChildren(ctx context.Context, botID string, target Session) ([]Ref, error) {
	parent, err := s.q.GetSession(ctx, target.ID)
	if err != nil {
		return nil, err
	}
	if parent.BotID != botID || parent.ParentID.Valid || parent.ChannelKey != target.ChannelKey {
		return nil, ErrChannelUnauthorized
	}
	children, err := s.q.ListChannelChildren(ctx, sqlc.ListChannelChildrenParams{BotID: parent.BotID, ParentID: sql.NullInt64{Int64: parent.ID, Valid: true}})
	if err != nil {
		return nil, err
	}
	out := make([]Ref, 0, len(children))
	for _, child := range children {
		ref, err := s.ref(ctx, s.q, child)
		if err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, nil
}

// ResumeChildFromSource queues a turn in an existing direct child. The new turn keeps the
// child's original parent source. turnKey must be stable across replays of the same command.
func (s *Service) ResumeChildFromSource(ctx context.Context, botID string, target Session, peer, source, turnKey, id, message string, identity ChannelContext) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.checkChannelCommand(ctx, q, source, botID, target.ID); err != nil {
		return err
	}
	if err := s.checkChannelInput(ctx, q, botID, peer, identity); err != nil {
		return err
	}
	parent, err := q.GetSession(ctx, target.ID)
	if err != nil {
		return err
	}
	if parent.BotID != botID || parent.ParentID.Valid || parent.ChannelKey != target.ChannelKey {
		return ErrChannelUnauthorized
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
	if original.SessionID != parent.ID || original.Origin != peer || original.ChannelRevoked != 0 || actor.AccountID != identity.AccountID || actor.SenderID != identity.SenderID {
		return fmt.Errorf("%w: only the initiating actor can resume this saved child", ErrChannelUnauthorized)
	}
	_, err = q.CreateParentTurn(ctx, sqlc.CreateParentTurnParams{ID: turnID(turnKey), SessionID: child.ID, Content: message, WhenBusy: "followUp", ParentRequestID: sql.NullString{String: original.ID, Valid: true}, CreatedAt: s.now().UnixMilli()})
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
