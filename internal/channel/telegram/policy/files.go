package policy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/upload"
)

// QueueFile binds bytes to the initiating Telegram input. Web and child calls keep URL-only delivery.
func (p *Policy) QueueFile(ctx context.Context, caller engine.TaskContext, name string, reader io.Reader) (bool, error) {
	if caller.BotID != p.botID || caller.Key == "" {
		return false, errors.New("invalid file delivery caller")
	}
	conv, err := p.q.GetSessionByExecution(ctx, sqlc.GetSessionByExecutionParams{BotID: p.botID, ExecutionID: sql.NullInt64{Int64: caller.ConversationID, Valid: true}})
	if err != nil {
		return false, err
	}
	source, err := p.q.GetSubmission(ctx, caller.SourceRequestID)
	if err != nil {
		return false, err
	}
	if source.SessionID != conv.ID {
		return false, errors.New("file source belongs to another conversation")
	}
	if conv.ParentID.Valid || !strings.HasPrefix(source.Origin, peerPrefix) {
		return false, nil
	}
	if source.Status != "pending" || source.ChannelRevoked != 0 {
		return false, errors.New("file delivery source is no longer active or authorized")
	}
	body, err := io.ReadAll(io.LimitReader(reader, upload.MaxSize+1))
	if err != nil {
		return false, err
	}
	if len(body) > upload.MaxSize {
		return false, upload.ErrTooLarge
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	n, err := p.q.QueueOutboxFile(ctx, sqlc.QueueOutboxFileParams{
		ID: uuid.NewV7().String(), OperationKey: caller.Key, SubmissionID: source.ID,
		BotID: p.botID, ExecutionID: sql.NullInt64{Int64: caller.ConversationID, Valid: true},
		Name: name, MimeType: http.DetectContentType(body), Body: body,
	})
	if err != nil {
		return false, err
	}
	if n == 0 {
		if _, err := p.q.GetOutboxFileBySubmission(ctx, sqlc.GetOutboxFileBySubmissionParams{OperationKey: caller.Key, SubmissionID: sql.NullString{String: source.ID, Valid: true}}); err != nil {
			return false, fmt.Errorf("cannot queue file: source ended, authorization changed, or 10 files are already queued: %w", err)
		}
	}
	return true, nil
}
