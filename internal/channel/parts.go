package channel

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/upload"
)

// Part is a rendered text message or one native media delivery.
type Part struct {
	Text string `json:"text,omitempty"`
	File *File  `json:"file,omitempty"`
}

// File identifies a temporary outbox snapshot, not an artifact entry.
type File struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
	Body     []byte `json:"-"`
}

// Receipt contains the message IDs confirmed by a platform call.
type Receipt struct{ MessageIDs []string }

// TextParts keeps text-only adapters on the shared part contract.
func TextParts(texts []string) []Part {
	parts := make([]Part, len(texts))
	for i, text := range texts {
		parts[i].Text = text
	}
	return parts
}

// RetryError keeps a failed part until the platform's flood-control window ends.
type RetryError struct {
	After time.Duration
	Err   error
}

func (e *RetryError) Error() string { return e.Err.Error() }
func (e *RetryError) Unwrap() error { return e.Err }

// QueueFile binds bytes to the initiating Telegram input. Web and child calls keep URL-only delivery.
func (m *Manager) QueueFile(ctx context.Context, caller engine.TaskContext, name string, reader io.Reader) (bool, error) {
	if caller.AgentID != m.agentID || caller.Key == "" {
		return false, errors.New("invalid file delivery caller")
	}
	conv, err := m.q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: m.agentID, ExecutionID: sql.NullInt64{Int64: caller.ConversationID, Valid: true}})
	if err != nil {
		return false, err
	}
	source, err := m.q.GetSubmission(ctx, caller.SourceRequestID)
	if err != nil {
		return false, err
	}
	if source.ConversationID != conv.ID {
		return false, errors.New("file source belongs to another conversation")
	}
	if conv.ParentID.Valid || !strings.HasPrefix(source.Origin, "telegram:") {
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
	n, err := m.q.QueueChannelReplyFile(ctx, sqlc.QueueChannelReplyFileParams{
		OperationKey: caller.Key, SubmissionID: source.ID, AgentID: m.agentID, ExecutionID: sql.NullInt64{Int64: caller.ConversationID, Valid: true},
		Name: name, MimeType: http.DetectContentType(body), Body: body,
	})
	if err != nil {
		return false, err
	}
	if n == 0 {
		if _, err := m.q.GetChannelReplyFile(ctx, sqlc.GetChannelReplyFileParams{OperationKey: caller.Key, SubmissionID: source.ID}); err != nil {
			return false, fmt.Errorf("cannot queue file: source ended, authorization changed, or 10 files are already queued: %w", err)
		}
	}
	return true, nil
}

func (m *Manager) replyParts(ctx context.Context, bot Bot, r sqlc.ListChannelRepliesRow) ([]Part, error) {
	var parts []Part
	if r.RenderedReply != "" {
		err := json.Unmarshal([]byte(r.RenderedReply), &parts)
		return parts, err
	}
	parts = bot.Parts(chat.Reply{Status: r.Status, Answer: r.Answer.String, Reason: r.Reason.String})
	if r.Status == "done" {
		files, err := m.q.ListChannelReplyFiles(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			parts = append(parts, Part{File: &File{Key: f.OperationKey, Name: f.Name, MimeType: f.MimeType, Size: f.Size.Int64}})
		}
	}
	encoded, err := json.Marshal(parts)
	if err != nil {
		return nil, err
	}
	if err := m.q.FreezeChannelReply(ctx, sqlc.FreezeChannelReplyParams{ID: r.ID, RenderedReply: string(encoded)}); err != nil {
		return nil, err
	}
	return parts, nil
}

func (m *Manager) authorizedReply(ctx context.Context, platform string, r sqlc.ListChannelRepliesRow) (bool, error) {
	if r.ChannelRevoked != 0 {
		return false, nil
	}
	if platform != "telegram" {
		return true, nil
	}
	cfg, err := m.q.GetTelegramSettings(ctx, m.agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var source chat.ChannelContext
	if err := json.Unmarshal([]byte(r.ChannelContext), &source); err != nil {
		return false, err
	}
	account, _, ok := strings.Cut(cfg.Token, ":")
	if !ok || source.AccountID != "" && source.AccountID != account {
		return false, nil
	}
	sender := source.SenderID
	if sender == "" {
		sender = strings.TrimPrefix(r.Origin, "telegram:")
	}
	id, err := strconv.ParseInt(sender, 10, 64)
	if err != nil || id <= 0 {
		return false, nil
	}
	for allowed := range strings.SplitSeq(cfg.AllowedUsers, ",") {
		if strings.TrimSpace(allowed) == sender {
			return true, nil
		}
	}
	return false, nil
}

func (m *Manager) confirmPart(ctx context.Context, r sqlc.ListChannelRepliesRow, receipt Receipt, remotes *[]string) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "UPDATE submissions SET id = id WHERE 0"); err != nil {
		return err
	}
	q := m.q.WithTx(tx)
	for _, id := range receipt.MessageIDs {
		if err := m.rememberQuestionMessage(ctx, q, r.ID, r.Origin, id); err != nil {
			return err
		}
	}
	next := append(append([]string{}, (*remotes)...), receipt.MessageIDs...)
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := q.AdvanceChannelReply(ctx, sqlc.AdvanceChannelReplyParams{ID: r.ID, RemoteIds: string(encoded)}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*remotes = next
	return nil
}
