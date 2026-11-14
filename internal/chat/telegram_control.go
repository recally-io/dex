package chat

import (
	"context"
	"encoding/json/v2"
	"strconv"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
)

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
	id, callback := strings.CutPrefix(source, "telegram-callback:")
	if !callback {
		return nil
	}
	if err := q.LockRoutineTools(ctx); err != nil {
		return err
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
	if !TelegramCallbackObject(row) || row.AgentID != agentID || row.ConversationID != conversationID || !row.ConsumedAt.Valid || row.ExpiresAt <= time.Now().UnixMilli() || command.AgentID != row.AgentID || command.Peer != row.Peer || command.Command != row.Action || command.ChannelKey != conv.ChannelKey || conv.AgentID != row.AgentID || conv.ExecutionScope != row.ExecutionScope || conv.ExecutionID.Int64 != row.ExecutionID || revision != row.ContextRevision {
		return ErrGroupUnauthorized
	}
	var actor, original ChannelContext
	if err := json.Unmarshal([]byte(command.ChannelContext), &actor); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(row.ChannelContext), &original); err != nil {
		return err
	}
	if actor.AccountID != original.AccountID || actor.SenderID != original.SenderID || actor.ChatID != original.ChatID || actor.TopicID != original.TopicID || actor.TopicShared != original.TopicShared || actor.MessageID != row.MessageID {
		return ErrGroupUnauthorized
	}
	allowed, err := TelegramInputAllowed(ctx, q, row.AgentID, row.Peer, actor)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrGroupUnauthorized
	}
	return nil
}
