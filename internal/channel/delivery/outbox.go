package delivery

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/upload"
)

func (m *Manager) flush(ctx context.Context, name string, bot channel.Bot) error {
	m.receiveMu.Lock()
	commands, err := m.q.ListChannelCommands(ctx, m.agentID)
	if err == nil {
		for _, c := range commands {
			if strings.HasPrefix(c.Peer, name+":") {
				if err = m.complete(ctx, c); err != nil {
					break
				}
			}
		}
	}
	m.receiveMu.Unlock()
	if err != nil {
		return err
	}
	acks, err := m.q.ListChannelCommandReplies(ctx, sqlc.ListChannelCommandRepliesParams{AgentID: m.agentID, Prefix: sql.NullString{String: name + ":", Valid: true}})
	if err != nil {
		return err
	}
	for _, r := range acks {
		if err := m.sendCommandReply(ctx, bot, r); err != nil {
			return err
		}
		if err := m.q.MarkChannelCommandDelivered(ctx, sqlc.MarkChannelCommandDeliveredParams{SourceKey: r.SourceKey, DeliveredAt: sql.NullInt64{Int64: time.Now().UnixMilli(), Valid: true}}); err != nil {
			return err
		}
	}
	replies, err := m.q.ListPendingOutboxMessages(ctx, sqlc.ListPendingOutboxMessagesParams{AgentID: m.agentID, OriginStart: name + ":", OriginEnd: name + ";"})
	if err != nil {
		return err
	}
	blocked := make(map[string]bool)
	for _, r := range replies {
		if blocked[r.Peer] {
			continue
		}
		if r.RetryAt > time.Now().UnixMilli() {
			blocked[r.Peer] = true
			continue
		}
		parts, err := m.replyParts(ctx, bot, r)
		if err != nil {
			return err
		}
		var remotes []channel.RemoteMessage
		if err := json.Unmarshal([]byte(r.RemoteIds), &remotes); err != nil {
			return err
		}
		for i := int(r.DeliveredParts); i < len(parts); i++ {
			allowed, err := m.authorizedReply(ctx, name, r.Peer, r.ChannelContext, r.SourceSubmissionID.String, r.ID)
			if err != nil {
				return err
			}
			if !allowed {
				if err := m.q.SetOutboxError(ctx, sqlc.SetOutboxErrorParams{ID: r.ID, Reason: "destination no longer authorized"}); err != nil {
					return err
				}
				break
			}
			err = m.sendReplyPart(ctx, name, bot, r, i, parts[i], &remotes)
			if err != nil {
				retryAt := time.Now().Add(5 * time.Second).UnixMilli()
				if flood, ok := errors.AsType[*channel.RetryError](err); ok {
					retryAt = time.Now().Add(flood.After).UnixMilli()
				}
				if stored := m.saveDeliveryError(ctx, r.ID, legacyReplySource(r), retryAt, err); stored != nil {
					return errors.Join(err, stored)
				}
				return err
			}
			r.GroupFallback, r.GroupCursor = 0, 0
		}
		if finished, ok := bot.(TelegramHooks); ok {
			allowed, err := m.authorizedReply(ctx, name, r.Peer, r.ChannelContext, r.SourceSubmissionID.String, r.ID)
			if err != nil {
				return err
			}
			if allowed {
				var source chat.ChannelContext
				if err := json.Unmarshal([]byte(r.ChannelContext), &source); err != nil {
					return err
				}
				if err := finished.Finish(ctx, r.Peer, source, outboxReplyStatus(r)); err != nil {
					// A final reaction retries independently of the legacy reply cursor.
					if saved := m.saveDeliveryError(ctx, r.ID, sql.NullString{}, time.Now().Add(5*time.Second).UnixMilli(), err); saved != nil {
						return errors.Join(err, saved)
					}
					return err
				}
			}
		}
		now := time.Now().UnixMilli()
		if err := m.q.MarkOutboxDelivered(ctx, sqlc.MarkOutboxDeliveredParams{ID: r.ID, DeliveredAt: sql.NullInt64{Int64: now, Valid: true}}); err != nil {
			return err
		}
		if source := legacyReplySource(r); source.Valid {
			if err := m.q.MarkChannelReplyDelivered(ctx, sqlc.MarkChannelReplyDeliveredParams{ID: source.String, DeliveredAt: sql.NullInt64{Int64: now, Valid: true}}); err != nil {
				return err
			}
		}
	}
	return nil
}

// rememberQuestionMessage links a sent message to the question that its input asked.
func (m *Manager) rememberQuestionMessage(ctx context.Context, q *sqlc.Queries, submissionID, peer, remoteID string) error {
	if remoteID == "" {
		return nil
	}
	question, err := q.GetQuestionBySource(ctx, submissionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return q.AddQuestionMessage(ctx, sqlc.AddQuestionMessageParams{AgentID: m.agentID, Peer: peer, RemoteID: remoteID, QuestionID: question.ID})
}

func (m *Manager) sendPart(ctx context.Context, bot channel.Bot, peer, id string, index int, part channel.Part) (channel.Receipt, error) {
	var err error
	if part.File != nil {
		file := *part.File
		part.File = &file
		part.File.Body, err = m.q.GetOutboxFile(ctx, sqlc.GetOutboxFileParams{OperationKey: part.File.Key, OutboxID: sql.NullString{String: id, Valid: true}})
		if errors.Is(err, sql.ErrNoRows) {
			err = fmt.Errorf("%w: file snapshot is no longer available", channel.ErrPermanent)
		}
	}
	if len(part.Group) > 0 {
		files := slices.Clone(part.Group)
		for i := range files {
			files[i].Body, err = m.q.GetOutboxFile(ctx, sqlc.GetOutboxFileParams{OperationKey: files[i].Key, OutboxID: sql.NullString{String: id, Valid: true}})
			if errors.Is(err, sql.ErrNoRows) {
				err = fmt.Errorf("%w: group snapshot is no longer available", channel.ErrPermanent)
			}
			if err != nil {
				break
			}
		}
		part.Group = files
	}
	var receipt channel.Receipt
	if err == nil {
		receipt, err = bot.Send(ctx, peer, id, index, part)
	}
	if errors.Is(err, channel.ErrPermanent) {
		slog.Warn("channel: skipped a rejected reply part", "delivery", id, "part", index, "err", err)
		return channel.Receipt{}, m.saveDeliveryError(ctx, id, sql.NullString{String: id, Valid: true}, 0, err)
	}
	if err == nil && len(part.Buttons) > 0 && len(receipt.MessageIDs) == 1 {
		err = m.q.BindTelegramCallbackMessage(ctx, sqlc.BindTelegramCallbackMessageParams{SourceID: id, Part: int64(index), MessageID: receipt.MessageIDs[0]})
	}
	return receipt, err
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
