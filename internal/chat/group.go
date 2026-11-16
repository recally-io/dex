package chat

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/upload"
)

var ErrGroupUnauthorized = errors.New("telegram group source is not authorized")

type groupBinding struct {
	AccountID   string `json:"accountId"`
	ChatID      int64  `json:"chatId"`
	TopicID     int64  `json:"topicId"`
	SenderID    string `json:"senderId"`
	TopicShared bool   `json:"topicShared,omitzero"`
}

// groupTarget derives routing from transport identity. The resulting scope is not an access grant.
func groupTarget(peer string, source ChannelContext) (key, scope, binding string, err error) {
	if source.ChatID >= 0 || source.TopicID < 0 || !positiveTelegramID(source.AccountID) || !positiveTelegramID(source.SenderID) {
		return "", "", "", ErrGroupUnauthorized
	}
	expectedPeer := fmt.Sprintf("telegram:%d", source.ChatID)
	if source.TopicID > 0 {
		expectedPeer += ":" + strconv.FormatInt(source.TopicID, 10)
	}
	if peer != expectedPeer {
		return "", "", "", ErrGroupUnauthorized
	}
	scope, err = engine.TelegramGroupScope(source.AccountID, source.ChatID, source.TopicID)
	if err != nil {
		return "", "", "", ErrGroupUnauthorized
	}
	audience := groupBinding{AccountID: source.AccountID, ChatID: source.ChatID, TopicID: source.TopicID, SenderID: source.SenderID}
	key = "telegram-group:" + scope + ":" + source.SenderID
	if source.TopicID > 0 || source.TopicShared {
		audience.TopicShared = source.TopicShared && source.TopicID == 0
		audience.SenderID = ""
		key = "telegram-topic:" + scope
	}
	encoded, err := json.Marshal(audience)
	return key, scope, string(encoded), err
}

func positiveTelegramID(value string) bool {
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

func groupPeer(peer string) bool { return strings.HasPrefix(peer, "telegram:-") }

func authorizeGroupSource(ctx context.Context, q *sqlc.Queries, conv sqlc.Conversation, source sqlc.Submission) error {
	if conv.ExecutionScope == "" || source.ConversationID != conv.ID || source.ChannelRevoked != 0 {
		return ErrGroupUnauthorized
	}
	if conv.ParentID.Valid {
		// One direct delegation retains the immutable initiating source, not an owner grant.
		parent, err := q.GetConversation(ctx, conv.ParentID.Int64)
		if err != nil {
			return err
		}
		if parent.ParentID.Valid || parent.AgentID != conv.AgentID || parent.ExecutionScope != conv.ExecutionScope || parent.ScopeContext != conv.ScopeContext || !source.ParentRequestID.Valid || source.Origin != "task" {
			return ErrGroupUnauthorized
		}
		original, err := q.GetSubmission(ctx, source.ParentRequestID.String)
		if err != nil {
			return err
		}
		return authorizeGroupSource(ctx, q, parent, original)
	}
	var identity ChannelContext
	if err := json.Unmarshal([]byte(source.ChannelContext), &identity); err != nil {
		return errors.Join(ErrGroupUnauthorized, err)
	}
	key, scope, binding, err := groupTarget(source.Origin, identity)
	if err != nil || scope != conv.ExecutionScope || key != conv.ChannelKey || binding != conv.ScopeContext {
		return ErrGroupUnauthorized
	}
	return authorizeTelegramActor(ctx, q, conv.AgentID, identity.AccountID, identity.SenderID)
}

func authorizeTelegramActor(ctx context.Context, q *sqlc.Queries, agentID, accountID, senderID string) error {
	if !positiveTelegramID(accountID) || !positiveTelegramID(senderID) {
		return ErrGroupUnauthorized
	}
	if err := db.RequireActive(ctx, q, agentID); err != nil {
		if errors.Is(err, db.ErrAgentArchived) {
			return errors.Join(ErrGroupUnauthorized, err)
		}
		return err
	}
	cfg, err := q.GetTelegramSettings(ctx, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGroupUnauthorized
	}
	if err != nil {
		return err
	}
	account, _, ok := strings.Cut(cfg.Token, ":")
	if !ok || account != accountID {
		return ErrGroupUnauthorized
	}
	for allowed := range strings.SplitSeq(cfg.AllowedUsers, ",") {
		if strings.TrimSpace(allowed) == senderID {
			return nil
		}
	}
	return ErrGroupUnauthorized
}

// TelegramInputAllowed checks current pairing before a transport downloads any bytes.
func (s *Service) TelegramInputAllowed(ctx context.Context, agentID, peer string, source ChannelContext) (bool, error) {
	return TelegramInputAllowed(ctx, s.q, agentID, peer, source)
}

// TelegramInputAllowed also serves writer transactions that commit Telegram control intents.
func TelegramInputAllowed(ctx context.Context, q *sqlc.Queries, agentID, peer string, source ChannelContext) (bool, error) {
	if groupPeer(peer) {
		if _, _, _, err := groupTarget(peer, source); err != nil {
			if errors.Is(err, ErrGroupUnauthorized) {
				return false, nil
			}
			return false, err
		}
	} else if !strings.HasPrefix(peer, "telegram:") || !positiveTelegramID(strings.TrimPrefix(peer, "telegram:")) || source.ChatID != 0 || source.TopicID != 0 || source.TopicShared {
		return false, nil
	}
	err := authorizeTelegramActor(ctx, q, agentID, source.AccountID, source.SenderID)
	if errors.Is(err, ErrGroupUnauthorized) {
		return false, nil
	}
	return err == nil, err
}

func (s *Service) groupUploads(agentID string, source ChannelContext) (*upload.Store, error) {
	scope, err := engine.TelegramGroupScope(source.AccountID, source.ChatID, source.TopicID)
	if err != nil {
		return nil, err
	}
	root, err := s.engine.GroupResources(agentID, scope)
	if err != nil {
		return nil, err
	}
	return upload.NewScoped(root), nil
}

// GroupUploads opens only the audience that current transport identity authorizes.
func (s *Service) GroupUploads(ctx context.Context, agentID, peer string, source ChannelContext) (*upload.Store, error) {
	if !groupPeer(peer) {
		return nil, ErrGroupUnauthorized
	}
	allowed, err := s.TelegramInputAllowed(ctx, agentID, peer, source)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrGroupUnauthorized
	}
	return s.groupUploads(agentID, source)
}

// RemoveGroupUploads is cleanup of persisted member ownership, not an actor tool.
func (s *Service) RemoveGroupUploads(agentID string, identity ChannelContext, files []engine.Attachment) error {
	store, err := s.groupUploads(agentID, identity)
	if err != nil {
		return err
	}
	defer store.Close()
	for _, file := range files {
		if err := store.Remove(file.ID); err != nil && !errors.Is(err, upload.ErrNotFound) {
			return err
		}
	}
	return nil
}

func discardGroupAlbums(ctx context.Context, q *sqlc.Queries, conv sqlc.Conversation) error {
	var binding groupBinding
	if err := json.Unmarshal([]byte(conv.ScopeContext), &binding); err != nil {
		return err
	}
	scope, err := engine.TelegramGroupScope(binding.AccountID, binding.ChatID, binding.TopicID)
	if err != nil || scope != conv.ExecutionScope {
		return ErrGroupUnauthorized
	}
	peer := fmt.Sprintf("telegram:%d", binding.ChatID)
	if binding.TopicID > 0 {
		peer += ":" + strconv.FormatInt(binding.TopicID, 10)
	}
	var shared int64
	if binding.TopicShared {
		shared = 1
	}
	return q.DiscardGroupTelegramAlbums(ctx, sqlc.DiscardGroupTelegramAlbumsParams{AgentID: conv.AgentID, AccountID: binding.AccountID, Peer: peer, SenderID: binding.SenderID, TopicID: binding.TopicID, TopicShared: shared})
}

// GroupConversation resolves only the current audience/session. It does not open native execution.
func (s *Service) GroupConversation(ctx context.Context, agentID, peer string, identity ChannelContext) (Chat, error) {
	key, scope, binding, err := groupTarget(peer, identity)
	if err != nil {
		return Chat{}, err
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return Chat{}, err
	}
	tx, q, err := s.writeTransaction(ctx)
	if err != nil {
		return Chat{}, err
	}
	defer func() { _ = tx.Rollback() }()
	conv, err := q.EnsureConversation(ctx, sqlc.EnsureConversationParams{AgentID: agentID, ChannelKey: key, ExecutionScope: scope, ScopeContext: binding, CreatedAt: s.now().UnixMilli()})
	if err != nil {
		return Chat{}, err
	}
	if err := authorizeGroupSource(ctx, q, conv, sqlc.Submission{ConversationID: conv.ID, Origin: peer, ChannelContext: string(encoded)}); err != nil {
		return Chat{}, err
	}
	if err := tx.Commit(); err != nil {
		return Chat{}, err
	}
	return s.Get(ctx, agentID, conv.ID)
}

func groupCaller(ctx context.Context, q *sqlc.Queries, caller engine.TaskContext) (sqlc.Conversation, sqlc.Submission, error) {
	if caller.Scope == "" {
		return sqlc.Conversation{}, sqlc.Submission{}, ErrGroupUnauthorized
	}
	conv, err := q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: caller.AgentID, ExecutionScope: caller.Scope, ExecutionID: sql.NullInt64{Int64: caller.ConversationID, Valid: true}})
	if err != nil {
		return conv, sqlc.Submission{}, err
	}
	source, err := q.GetSubmission(ctx, caller.SourceRequestID)
	if err != nil {
		return conv, source, err
	}
	if source.Status != "pending" {
		return conv, source, ErrGroupUnauthorized
	}
	return conv, source, authorizeGroupSource(ctx, q, conv, source)
}

// AuthorizeGroup checks the current actor grant at execution, including restored native tools.
func (s *Service) AuthorizeGroup(ctx context.Context, caller engine.TaskContext) error {
	_, _, err := groupCaller(ctx, s.q, caller)
	return err
}

// GroupReplyAllowed checks source ownership again after execution, before a public send.
func (s *Service) GroupReplyAllowed(ctx context.Context, agentID, sourceID string) (bool, error) {
	source, err := s.q.GetSubmission(ctx, sourceID)
	if err != nil {
		return false, err
	}
	conv, err := s.q.GetConversation(ctx, source.ConversationID)
	if err != nil {
		return false, err
	}
	if conv.AgentID != agentID || conv.ExecutionScope == "" {
		return false, nil
	}
	err = authorizeGroupSource(ctx, s.q, conv, source)
	if errors.Is(err, ErrGroupUnauthorized) {
		return false, nil
	}
	return err == nil, err
}
