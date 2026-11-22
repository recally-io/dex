package delivery

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
	"uuid"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/upload"
)

// QueueFile binds bytes to the initiating Telegram input. Web and child calls keep URL-only delivery.
func (m *Manager) QueueFile(ctx context.Context, caller engine.TaskContext, name string, reader io.Reader) (bool, error) {
	if caller.AgentID != m.agentID || caller.Key == "" {
		return false, errors.New("invalid file delivery caller")
	}
	if caller.Scope != "" {
		if err := m.chats.AuthorizeGroup(ctx, caller); err != nil {
			return false, err
		}
	}
	conv, err := m.q.GetConversationByExecution(ctx, sqlc.GetConversationByExecutionParams{AgentID: m.agentID, ExecutionScope: caller.Scope, ExecutionID: sql.NullInt64{Int64: caller.ConversationID, Valid: true}})
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
	n, err := m.q.QueueOutboxFile(ctx, sqlc.QueueOutboxFileParams{
		ID: uuid.NewV7().String(), OperationKey: caller.Key, SubmissionID: source.ID,
		AgentID: m.agentID, ExecutionScope: caller.Scope, ExecutionID: sql.NullInt64{Int64: caller.ConversationID, Valid: true},
		Name: name, MimeType: http.DetectContentType(body), Body: body,
	})
	if err != nil {
		return false, err
	}
	if n == 0 {
		if _, err := m.q.GetOutboxFileBySubmission(ctx, sqlc.GetOutboxFileBySubmissionParams{OperationKey: caller.Key, SubmissionID: sql.NullString{String: source.ID, Valid: true}}); err != nil {
			return false, fmt.Errorf("cannot queue file: source ended, authorization changed, or 10 files are already queued: %w", err)
		}
	}
	return true, nil
}

func outboxReplyStatus(r sqlc.ListPendingOutboxMessagesRow) string {
	if r.Reason != "" || r.Kind == "system_error" {
		return "unanswered"
	}
	return "done"
}

func (m *Manager) replyParts(ctx context.Context, bot channel.Bot, r sqlc.ListPendingOutboxMessagesRow) ([]channel.Part, error) {
	var parts []channel.Part
	if r.RenderedParts != "" {
		err := json.Unmarshal([]byte(r.RenderedParts), &parts)
		return parts, err
	}
	status := outboxReplyStatus(r)
	parts = bot.Parts(channel.Reply{Status: status, Answer: r.Body, Reason: r.Reason})
	if status == "done" {
		files, err := m.q.ListOutboxFiles(ctx, sql.NullString{String: r.ID, Valid: true})
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			parts = append(parts, channel.Part{File: &channel.File{Key: f.OperationKey, Name: f.Name, MimeType: f.MimeType, Size: f.Size.Int64}})
		}
	}
	if group, ok := bot.(TelegramHooks); ok {
		for i := range parts {
			if file := parts[i].File; file != nil {
				var err error
				file.Body, err = m.q.GetOutboxFile(ctx, sqlc.GetOutboxFileParams{OperationKey: file.Key, OutboxID: sql.NullString{String: r.ID, Valid: true}})
				if err != nil {
					return nil, err
				}
			}
		}
		parts = group.GroupParts(parts)
	}
	if strings.HasPrefix(r.Peer, "telegram:") && r.SourceSubmissionID.Valid {
		question, err := m.q.GetQuestionBySource(ctx, r.SourceSubmissionID.String)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			var identity chat.ChannelContext
			if err := json.Unmarshal([]byte(r.ChannelContext), &identity); err != nil {
				return nil, err
			}
			target, err := m.chats.ChannelConversation(ctx, m.agentID, r.Peer, identity)
			if err != nil {
				if errors.Is(err, chat.ErrGroupUnauthorized) {
					return parts, nil
				}
				return nil, err
			}
			actions, err := questionActions(question)
			if err != nil {
				return nil, err
			}
			parts, err = m.addButtons(ctx, parts, r.SourceSubmissionID.String, r.Peer, identity, target, actions)
			if err != nil {
				return nil, err
			}
		}
	}
	encoded, err := json.Marshal(parts)
	if err != nil {
		return nil, err
	}
	if err := m.q.FreezeOutboxMessage(ctx, sqlc.FreezeOutboxMessageParams{ID: r.ID, RenderedParts: string(encoded)}); err != nil {
		return nil, err
	}
	return parts, nil
}

func (m *Manager) authorizedReply(ctx context.Context, platform, peer, channelContext, sourceSubmissionID, outboxID string) (bool, error) {
	if outboxID != "" {
		msg, err := m.q.GetOutboxMessage(ctx, outboxID)
		if err != nil {
			return false, err
		}
		if msg.Status != "pending" {
			return false, nil
		}
	}
	if sourceSubmissionID != "" {
		current, err := m.q.GetSubmission(ctx, sourceSubmissionID)
		if err != nil {
			return false, err
		}
		if current.ChannelRevoked != 0 {
			return false, nil
		}
	}
	if platform != "telegram" {
		return true, nil
	}
	if strings.HasPrefix(peer, "telegram:-") {
		if sourceSubmissionID == "" {
			return false, nil
		}
		return m.chats.GroupReplyAllowed(ctx, m.agentID, sourceSubmissionID)
	}
	cfg, err := m.q.GetTelegramSettings(ctx, m.agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var source chat.ChannelContext
	if err := json.Unmarshal([]byte(channelContext), &source); err != nil {
		return false, err
	}
	account, _, ok := strings.Cut(cfg.Token, ":")
	if !ok || source.AccountID != "" && source.AccountID != account {
		return false, nil
	}
	sender := source.SenderID
	if sender == "" {
		sender = strings.TrimPrefix(peer, "telegram:")
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

// legacyReplySource identifies the submissions row whose delivery cursor mirrors the outbox.
func legacyReplySource(r sqlc.ListPendingOutboxMessagesRow) sql.NullString {
	if r.Kind == "reply" || r.Kind == "question" {
		return r.SourceSubmissionID
	}
	return sql.NullString{}
}

func (m *Manager) saveDeliveryError(ctx context.Context, id string, legacySource sql.NullString, retryAt int64, cause error) error {
	reason := cause.Error()
	if err := m.q.SetOutboxError(ctx, sqlc.SetOutboxErrorParams{ID: id, RetryAt: retryAt, Reason: reason}); err != nil {
		return err
	}
	if legacySource.Valid {
		return m.q.SetChannelReplyError(ctx, sqlc.SetChannelReplyErrorParams{ID: legacySource.String, RetryAt: retryAt, Reason: reason})
	}
	return nil
}

func (m *Manager) confirmDelivery(ctx context.Context, r sqlc.ListPendingOutboxMessagesRow, receipt channel.Receipt, remotes *[]channel.RemoteMessage, member bool) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "UPDATE submissions SET id = id WHERE 0"); err != nil {
		return err
	}
	q := m.q.WithTx(tx)
	if r.SourceSubmissionID.Valid {
		for _, id := range receipt.MessageIDs {
			if err := m.rememberQuestionMessage(ctx, q, r.SourceSubmissionID.String, r.Peer, id); err != nil {
				return err
			}
		}
	}
	next := append([]channel.RemoteMessage{}, (*remotes)...)
	for _, id := range receipt.MessageIDs {
		next = append(next, channel.RemoteMessage{ID: id, Kind: receipt.Kind})
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	source := legacyReplySource(r)
	if member {
		err = q.AdvanceOutboxGroupMember(ctx, sqlc.AdvanceOutboxGroupMemberParams{ID: r.ID, RemoteIds: string(encoded)})
		if err == nil && source.Valid {
			err = q.AdvanceChannelGroupMember(ctx, sqlc.AdvanceChannelGroupMemberParams{ID: source.String, RemoteIds: string(encoded)})
		}
	} else {
		err = q.AdvanceOutboxMessage(ctx, sqlc.AdvanceOutboxMessageParams{ID: r.ID, RemoteIds: string(encoded)})
		if err == nil && source.Valid {
			err = q.AdvanceChannelReply(ctx, sqlc.AdvanceChannelReplyParams{ID: source.String, RemoteIds: string(encoded)})
		}
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*remotes = next
	return nil
}
