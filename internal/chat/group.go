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
)

var ErrGroupUnauthorized = errors.New("telegram group source is not authorized")

type groupBinding struct {
	AccountID string `json:"accountId"`
	ChatID    int64  `json:"chatId"`
	TopicID   int64  `json:"topicId"`
	SenderID  string `json:"senderId"`
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
	if source.TopicID > 0 {
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
	if err := db.RequireActive(ctx, q, conv.AgentID); err != nil {
		if errors.Is(err, db.ErrAgentArchived) {
			return errors.Join(ErrGroupUnauthorized, err)
		}
		return err
	}
	var identity ChannelContext
	if err := json.Unmarshal([]byte(source.ChannelContext), &identity); err != nil {
		return errors.Join(ErrGroupUnauthorized, err)
	}
	key, scope, binding, err := groupTarget(source.Origin, identity)
	if err != nil || scope != conv.ExecutionScope || key != conv.ChannelKey || binding != conv.ScopeContext {
		return ErrGroupUnauthorized
	}
	cfg, err := q.GetTelegramSettings(ctx, conv.AgentID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGroupUnauthorized
	}
	if err != nil {
		return err
	}
	account, _, ok := strings.Cut(cfg.Token, ":")
	if !ok || account != identity.AccountID {
		return ErrGroupUnauthorized
	}
	for allowed := range strings.SplitSeq(cfg.AllowedUsers, ",") {
		if strings.TrimSpace(allowed) == identity.SenderID {
			return nil
		}
	}
	return ErrGroupUnauthorized
}

// AuthorizeGroup checks the current actor grant at execution, including restored native tools.
func (s *Service) AuthorizeGroup(ctx context.Context, caller engine.TaskContext) error {
	if caller.Scope == "" {
		return ErrGroupUnauthorized
	}
	conv, err := s.q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: caller.AgentID, ExecutionScope: caller.Scope, ExecutionID: sql.NullInt64{Int64: caller.ConversationID, Valid: true}})
	if err != nil {
		return err
	}
	source, err := s.q.GetSubmission(ctx, caller.SourceRequestID)
	if err != nil {
		return err
	}
	if source.Status != "pending" {
		return ErrGroupUnauthorized
	}
	return authorizeGroupSource(ctx, s.q, conv, source)
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
