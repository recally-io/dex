package delivery

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/upload"
)

const (
	minOutboxAttemptsBeforeExpiry = 20
	maxOutboxRetryAge             = 24 * time.Hour
)

func (m *Manager) flush(ctx context.Context, name string, bot channel.Bot) error {
	commands, err := m.q.ListChannelCommands(ctx, m.agentID)
	if err != nil {
		return err
	}
	for _, c := range commands {
		if strings.HasPrefix(c.Peer, name+":") {
			if err := m.complete(ctx, c.SourceKey); err != nil {
				return err
			}
		}
	}
	acks, err := m.q.ListChannelCommandReplies(ctx, sqlc.ListChannelCommandRepliesParams{AgentID: m.agentID, Prefix: sql.NullString{String: name + ":", Valid: true}})
	if err != nil {
		return err
	}
	for _, r := range acks {
		if err := m.queueCommandReply(ctx, bot, r); err != nil {
			return err
		}
	}
	replies, err := m.q.ListPendingOutboxMessages(ctx, sqlc.ListPendingOutboxMessagesParams{AgentID: m.agentID, Channel: sql.NullString{String: name, Valid: true}})
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
		part, err := m.renderMessage(ctx, bot, r)
		if err == nil {
			active, checkErr := m.q.CanSendOutbox(ctx, r.ID)
			if checkErr != nil {
				return checkErr
			}
			if !active {
				continue
			}
			var receipt channel.Receipt
			receipt, err = bot.Send(ctx, r.Peer, r.ID, part)
			if err == nil {
				if name == "wechat" || receipt.MessageID != "" {
					if err := m.confirmDelivery(ctx, r, part, receipt); err != nil {
						return err
					}
					continue
				}
				err = errors.New("platform did not confirm one message")
			}
		}
		delay := 5 * time.Second << min(r.Attempts, 6)
		if flood, ok := errors.AsType[*channel.RetryError](err); ok {
			delay = max(delay, flood.After)
		}
		if r.Kind == "feedback" {
			// A platform error can quote the private draft.
			if errors.Is(err, channel.ErrPermanent) {
				err = channel.ErrPermanent
			} else {
				err = errors.New("feedback reply delivery failed")
			}
		}
		expired := r.Attempts+1 >= minOutboxAttemptsBeforeExpiry && time.Now().UnixMilli()-r.CreatedAt >= maxOutboxRetryAge.Milliseconds()
		if errors.Is(err, channel.ErrPermanent) || expired {
			if err := m.q.FailOutboxMessage(ctx, sqlc.FailOutboxMessageParams{ID: r.ID, Reason: err.Error()}); err != nil {
				return err
			}
			continue
		}
		if stored := m.q.SetOutboxError(ctx, sqlc.SetOutboxErrorParams{ID: r.ID, RetryAt: time.Now().Add(delay).UnixMilli(), Reason: err.Error()}); stored != nil {
			return errors.Join(err, stored)
		}
		blocked[r.Peer] = true
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

func (m *Manager) renderMessage(ctx context.Context, bot channel.Bot, r sqlc.OutboxMessage) (channel.Part, error) {
	if r.Kind == "file" {
		file, err := m.q.GetOutboxFile(ctx, sql.NullString{String: r.ID, Valid: true})
		if errors.Is(err, sql.ErrNoRows) {
			return channel.Part{}, fmt.Errorf("%w: file snapshot is no longer available", channel.ErrPermanent)
		}
		return channel.Part{File: &channel.File{Key: file.OperationKey, Name: file.Name, MimeType: file.MimeType, Size: file.Size.Int64, Body: file.Body}}, err
	}
	if r.RenderedMessage != "" {
		var part channel.Part
		err := json.Unmarshal([]byte(r.RenderedMessage), &part)
		return part, err
	}
	parts := finalMessage(bot, r.Body, r.Reason)
	if len(parts) != 1 {
		return channel.Part{}, fmt.Errorf("%w: cannot render one platform message", channel.ErrPermanent)
	}
	if r.Kind == "question" && strings.HasPrefix(r.Peer, "telegram:") && r.SourceSubmissionID.Valid {
		question, err := m.q.GetQuestionBySource(ctx, r.SourceSubmissionID.String)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return channel.Part{}, err
		}
		if err == nil {
			var identity chat.ChannelContext
			if err := json.Unmarshal([]byte(r.ChannelContext), &identity); err != nil {
				return channel.Part{}, err
			}
			conv, err := m.q.GetConversation(ctx, r.ConversationID)
			if err != nil {
				return channel.Part{}, err
			}
			target := chat.Chat{ID: conv.ID, ExecutionID: conv.ExecutionID.Int64}
			actions, err := questionActions(question)
			if err != nil {
				return channel.Part{}, err
			}
			parts, err = m.addButtons(ctx, parts, r.SourceSubmissionID.String, r.Peer, identity, target, actions)
			if err != nil {
				return channel.Part{}, err
			}
		}
	}
	encoded, err := json.Marshal(parts[0])
	if err != nil {
		return channel.Part{}, err
	}
	err = m.q.FreezeOutboxMessage(ctx, sqlc.FreezeOutboxMessageParams{ID: r.ID, RenderedMessage: string(encoded)})
	return parts[0], err
}

func finalMessage(bot channel.Bot, body, reason string) []channel.Part {
	if reason != "" && body == "" {
		return bot.Parts(channel.Reply{Status: "unanswered", Reason: "The request did not complete. See Web for details."})
	}
	if len(body) <= 4000 {
		if parts := bot.Parts(channel.Reply{Status: "done", Answer: body}); len(parts) == 1 {
			return parts
		}
	}
	runes := []rune(body)
	for size := min(len(runes), 900); size > 0; size /= 2 {
		text := closeUnclosedCodeFence(strings.TrimSpace(string(runes[:size]))) + "\n\nFull answer in Web."
		if parts := bot.Parts(channel.Reply{Status: "done", Answer: text}); len(parts) == 1 {
			return parts
		}
	}
	return bot.Parts(channel.Reply{Status: "done", Answer: "Full answer in Web."})
}

func closeUnclosedCodeFence(s string) string {
	var openFence string
	for line := range strings.SplitSeq(s, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if openFence == "" {
			if strings.HasPrefix(trimmed, "```") {
				openFence = "```"
			} else if strings.HasPrefix(trimmed, "~~~") {
				openFence = "~~~"
			}
		} else if strings.HasPrefix(trimmed, openFence) {
			openFence = ""
		}
	}
	if openFence != "" {
		return s + "\n" + openFence
	}
	return s
}

func (m *Manager) confirmDelivery(ctx context.Context, r sqlc.OutboxMessage, part channel.Part, receipt channel.Receipt) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	remoteID := receipt.MessageID
	remotes := []channel.RemoteMessage{}
	if remoteID != "" {
		remotes = append(remotes, channel.RemoteMessage{ID: remoteID, Kind: receipt.Kind})
	}
	encoded, err := json.Marshal(remotes)
	if err != nil {
		return err
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := m.q.WithTx(tx)
	now := sql.NullInt64{Int64: time.Now().UnixMilli(), Valid: true}
	if err := q.MarkOutboxSent(ctx, sqlc.MarkOutboxSentParams{ID: r.ID, DeliveredAt: now, RemoteIds: string(encoded)}); err != nil {
		return err
	}
	if r.SourceSubmissionID.Valid && (r.Kind == "reply" || r.Kind == "question") {
		if err := m.rememberQuestionMessage(ctx, q, r.SourceSubmissionID.String, r.Peer, remoteID); err != nil {
			return err
		}
		if err := q.MarkChannelReplyDelivered(ctx, sqlc.MarkChannelReplyDeliveredParams{ID: r.SourceSubmissionID.String, DeliveredAt: now}); err != nil {
			return err
		}
	}
	if len(part.Buttons) != 0 {
		if err := q.BindTelegramCallbackMessage(ctx, sqlc.BindTelegramCallbackMessageParams{SourceID: cmp.Or(r.SourceSubmissionID.String, r.ID), Part: 0, MessageID: remoteID}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

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
	n, err := m.q.QueueOutboxFile(ctx, sqlc.QueueOutboxFileParams{
		ID: uuid.NewV7().String(), OperationKey: caller.Key, SubmissionID: source.ID,
		AgentID: m.agentID, ExecutionID: sql.NullInt64{Int64: caller.ConversationID, Valid: true},
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
