package session

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/dream"
	"github.com/recally-io/dex/internal/engine"
)

// ToolReadDreamMessage reads a page of one record included in the active dream.
func (s *Service) ToolReadDreamMessage(ctx context.Context, caller engine.TaskContext, args DreamMessageRequest) (DreamMessagePage, error) {
	denied := errors.New("memory source is unavailable")
	if args.Offset < 0 || (args.Field != "user" && args.Field != "answer") {
		return DreamMessagePage{}, denied
	}
	conv, err := s.q.GetSessionByExecution(ctx, sqlc.GetSessionByExecutionParams{BotID: caller.BotID, ExecutionID: sql.NullInt64{Int64: caller.ConversationID, Valid: true}})
	if err != nil || !strings.HasPrefix(conv.ChannelKey, dream.Channel) {
		return DreamMessagePage{}, denied
	}
	source, err := s.q.GetSubmission(ctx, caller.SourceRequestID)
	if err != nil || source.SessionID != conv.ID || source.Status != "pending" || !dream.ContainsMessage(source.Content, args.MessageID) {
		return DreamMessagePage{}, denied
	}
	message, err := s.q.GetSubmission(ctx, args.MessageID)
	if err != nil || message.Kind != "message" || message.Status == "pending" {
		return DreamMessagePage{}, denied
	}
	owner, err := s.q.GetSession(ctx, message.SessionID)
	if err != nil || owner.BotID != caller.BotID || strings.HasPrefix(owner.ChannelKey, dream.Channel) || owner.ParentID.Valid {
		return DreamMessagePage{}, denied
	}
	text := message.Content
	if args.Field == "answer" {
		text = message.Answer.String
	}
	runes := []rune(text)
	if args.Offset > len(runes) {
		return DreamMessagePage{}, denied
	}
	end := min(args.Offset+4000, len(runes))
	return DreamMessagePage{Text: string(runes[args.Offset:end]), NextOffset: end, HasMore: end < len(runes)}, nil
}
