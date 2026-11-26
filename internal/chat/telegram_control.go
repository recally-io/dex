package chat

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
)

var ErrTelegramUnauthorized = errors.New("telegram input is not authorized")

// TelegramCallbackObject checks the explicit object in the fixed Telegram command set.
func TelegramCallbackObject(row sqlc.TelegramCallback) bool {
	command, rest, _ := strings.Cut(row.Action, " ")
	if command == "/run" || command == "/answer" {
		object, _, _ := strings.Cut(rest, " ")
		return object != "" && object == row.ObjectID
	}
	switch command {
	case "/menu", "/help", "/status", "/settings", "/sessions", "/workflows", "/question", "/stop", "/clear", "/new":
		return row.ObjectID == strconv.FormatInt(row.ConversationID, 10)
	}
	return false
}

// checkTelegramControl runs under the effect's SQLite writer, after a callback queued its intent.
// It prevents a queued action from gaining a new context or a later authorization grant.
func checkTelegramControl(ctx context.Context, q *sqlc.Queries, source, agentID string, conversationID int64) error {
	if !strings.HasPrefix(source, "telegram-callback:") {
		return nil
	}
	if err := q.LockAutomationTools(ctx); err != nil {
		return err
	}
	return ValidateTelegramControl(ctx, q, source, agentID, conversationID)
}

// ValidateTelegramControl is the read-only validity check of a claimed callback command. Other
// sources pass. The command must keep its button's peer, conversation, generation, object,
// expiry, actor, and active binding. It returns ErrTelegramUnauthorized for a stale command.
// Effects must call it again under their writer through checkTelegramControl.
func ValidateTelegramControl(ctx context.Context, q *sqlc.Queries, source, agentID string, conversationID int64) error {
	id, callback := strings.CutPrefix(source, "telegram-callback:")
	if !callback {
		return nil
	}
	row, err := q.GetTelegramCallback(ctx, id)
	if err != nil {
		return err
	}
	command, err := q.GetChannelCommand(ctx, source)
	if err != nil {
		return err
	}
	conv, err := q.GetConversation(ctx, row.ConversationID)
	if err != nil {
		return err
	}
	revision, err := q.TelegramContextRevision(ctx, conv.ID)
	if err != nil {
		return err
	}
	if !TelegramCallbackObject(row) || row.AgentID != agentID || row.ConversationID != conversationID || !row.ConsumedAt.Valid || row.ExpiresAt <= time.Now().UnixMilli() || command.AgentID != row.AgentID || command.Peer != row.Peer || command.Command != row.Action || command.ChannelKey != conv.ChannelKey || conv.AgentID != row.AgentID || conv.ExecutionID.Int64 != row.ExecutionID || revision != row.ContextRevision {
		return ErrTelegramUnauthorized
	}
	var actor, original ChannelContext
	if err := json.Unmarshal([]byte(command.ChannelContext), &actor); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(row.ChannelContext), &original); err != nil {
		return err
	}
	if actor.AccountID != original.AccountID || actor.SenderID != original.SenderID || actor.ChatID != original.ChatID || actor.TopicID != original.TopicID || actor.TopicShared != original.TopicShared || actor.MessageID != row.MessageID {
		return ErrTelegramUnauthorized
	}
	return authorizeTelegramActor(ctx, q, agentID, actor.AccountID, actor.SenderID)
}

// NonPrivateTelegramInput rejects legacy group routes and actor metadata.
func NonPrivateTelegramInput(peer string, source ChannelContext) bool {
	return strings.HasPrefix(peer, "telegram:-") || source.ChatID != 0 || source.TopicID != 0 || source.TopicShared
}

func positiveTelegramID(value string) bool {
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

func authorizeTelegramActor(ctx context.Context, q *sqlc.Queries, agentID, accountID, senderID string) error {
	if !positiveTelegramID(accountID) || !positiveTelegramID(senderID) {
		return ErrTelegramUnauthorized
	}
	if err := db.RequireActive(ctx, q, agentID); err != nil {
		if errors.Is(err, db.ErrAgentArchived) {
			return errors.Join(ErrTelegramUnauthorized, err)
		}
		return err
	}
	cfg, err := q.GetTelegramSettings(ctx, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTelegramUnauthorized
	}
	if err != nil {
		return err
	}
	account, _, ok := strings.Cut(cfg.Token, ":")
	if !ok || account != accountID {
		return ErrTelegramUnauthorized
	}
	for allowed := range strings.SplitSeq(cfg.AllowedUsers, ",") {
		if strings.TrimSpace(allowed) == senderID {
			return nil
		}
	}
	return ErrTelegramUnauthorized
}

// TelegramInputAllowed checks current pairing before a transport downloads any bytes.
func (s *Service) TelegramInputAllowed(ctx context.Context, agentID, peer string, source ChannelContext) (bool, error) {
	return TelegramInputAllowed(ctx, s.q, agentID, peer, source)
}

// TelegramInputAllowed also serves writer transactions that commit Telegram control intents.
func TelegramInputAllowed(ctx context.Context, q *sqlc.Queries, agentID, peer string, source ChannelContext) (bool, error) {
	if !strings.HasPrefix(peer, "telegram:") || !positiveTelegramID(strings.TrimPrefix(peer, "telegram:")) || NonPrivateTelegramInput(peer, source) {
		return false, nil
	}
	err := authorizeTelegramActor(ctx, q, agentID, source.AccountID, source.SenderID)
	if errors.Is(err, ErrTelegramUnauthorized) {
		return false, nil
	}
	return err == nil, err
}

// ChannelConversation returns Main only for an authorized private Telegram actor.
func (s *Service) ChannelConversation(ctx context.Context, agentID, peer string, identity ChannelContext) (Chat, error) {
	allowed, err := s.TelegramInputAllowed(ctx, agentID, peer, identity)
	if err != nil {
		return Chat{}, err
	}
	if !allowed {
		return Chat{}, ErrTelegramUnauthorized
	}
	return s.Main(ctx, agentID)
}

// ChannelChildren lists the direct children of an already resolved channel target, never the
// private global session list.
func (s *Service) ChannelChildren(ctx context.Context, agentID string, target Chat) ([]SessionRef, error) {
	parent, err := s.q.GetConversation(ctx, target.ID)
	if err != nil {
		return nil, err
	}
	if parent.AgentID != agentID || parent.ParentID.Valid || parent.ChannelKey != target.ChannelKey {
		return nil, ErrTelegramUnauthorized
	}
	children, err := s.q.ListChannelChildren(ctx, sqlc.ListChannelChildrenParams{AgentID: parent.AgentID, ParentID: sql.NullInt64{Int64: parent.ID, Valid: true}})
	if err != nil {
		return nil, err
	}
	out := make([]SessionRef, 0, len(children))
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
	if err := q.LockAutomationTools(ctx); err != nil {
		return err
	}
	allowed, err := TelegramInputAllowed(ctx, q, agentID, peer, identity)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrTelegramUnauthorized
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
		return fmt.Errorf("%w: only the initiating Telegram actor resumes this saved child", ErrTelegramUnauthorized)
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
