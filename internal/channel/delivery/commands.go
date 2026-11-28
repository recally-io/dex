package delivery

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/session"
)

// CommandReceived reports whether the globally keyed command receipt exists.
// It does not execute the command or authorize a replay; Receive does that.
func (m *Manager) CommandReceived(ctx context.Context, source string) (bool, error) {
	_, err := m.q.GetChannelCommand(ctx, source)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Receive stores one update or executes a shared command. A repeated source has no effect.
func (m *Manager) Receive(ctx context.Context, peer, source string, msg session.Message) error {
	policy := m.policy(peer)
	if policy != nil {
		if err := policy.AdmitInput(peer); err != nil {
			return err
		}
	}
	if !strings.Contains(peer, ":") || strings.Contains(peer, "/") || source == "" || msg.ChannelContext.AccountID == "" {
		return errors.New("invalid channel peer, account, or source")
	}
	if len(msg.Attachments) > 10 {
		return errors.New("a message can have at most 10 attachments")
	}
	if received, err := m.ReceivedSource(ctx, source); err != nil {
		return err
	} else if received {
		return nil
	}
	_, err := m.q.GetChannelCommand(ctx, source)
	if err == nil {
		return m.CompleteCommand(ctx, source)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	cmd := strings.TrimSpace(msg.Content)
	isFeedback := channel.IsFeedbackCommand(cmd)
	basicCommand := cmd == "/clear" || cmd == "/new" || cmd == "/stop" || cmd == "/help"
	platformCommand := policy != nil && policy.IsCommand(cmd)
	isCommand := isFeedback || basicCommand || platformCommand
	if !isCommand || msg.AlbumSource != "" || len(msg.Attachments) > 0 {
		return m.sessions.AcceptMessage(ctx, m.botID, peer, source, msg)
	}
	if err := session.RequirePairedChannel(ctx, m.q, m.botID, peer, msg.ChannelContext); err != nil {
		return err
	}
	identity, err := json.Marshal(msg.ChannelContext)
	if err != nil {
		return err
	}
	if isFeedback {
		answer := feedbackReply(cmd)
		if err := m.q.CreateFeedbackCommand(ctx, sqlc.CreateFeedbackCommandParams{BotID: m.botID, SourceKey: source, Peer: peer, ChannelContext: string(identity), Answer: answer}); err != nil {
			return err
		}
		m.Notify(peer)
		return nil
	}
	if err = m.q.CreateChannelCommand(ctx, sqlc.CreateChannelCommandParams{BotID: m.botID, SourceKey: source, Peer: peer, ChannelKey: session.MainKey, Command: cmd, ChannelContext: string(identity)}); err != nil {
		return err
	}
	return m.CompleteCommand(ctx, source)
}

// CompleteCommand processes a stored command. The caller must first commit its receipt.
func (m *Manager) CompleteCommand(ctx context.Context, source string) error {
	// Polling and the outbox can replay the same command. Never hold a bot-wide
	// lock while the command calls a target Session.
	owned := make(chan struct{})
	for {
		m.mu.Lock()
		prior := m.commands[source]
		if prior == nil {
			m.commands[source] = owned
			m.mu.Unlock()
			break
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-prior:
		}
	}
	defer func() {
		m.mu.Lock()
		delete(m.commands, source)
		close(owned)
		m.mu.Unlock()
	}()
	c, err := m.q.GetChannelCommand(ctx, source)
	if err != nil {
		return err
	}
	if c.BotID != m.botID || c.Completed != 0 || c.DeliveredAt.Valid {
		return nil
	}
	runShared := func(ctx context.Context, targetID int64) (string, error) {
		return m.runSharedCommand(ctx, c, targetID)
	}
	var answer string
	wake := true
	if policy := m.policy(c.Peer); policy != nil {
		answer, wake, err = policy.RunCommand(ctx, c, runShared)
	} else {
		answer, err = runShared(ctx, 0)
	}
	if err != nil {
		return err
	}
	if err := m.q.CompleteChannelCommand(ctx, sqlc.CompleteChannelCommandParams{Answer: answer, SourceKey: c.SourceKey}); err != nil {
		return err
	}
	if wake {
		m.Notify(c.Peer)
	}
	return nil
}

func (m *Manager) runSharedCommand(ctx context.Context, c sqlc.ChannelCommand, targetID int64) (string, error) {
	// A saved Stop answer means the withdrawal already committed.
	if c.Command == "/stop" && c.Answer != "" {
		return c.Answer, nil
	}
	switch c.Command {
	case "/clear", "/new", "/stop":
		if targetID == 0 {
			target, err := m.sessions.Main(ctx, m.botID)
			if err != nil {
				return "", err
			}
			targetID = target.ID
		}
	default:
		return "/clear clears the main timeline's context. It takes no text. /new does the same. /stop stops the main answer and its background work. /feedback prepares a public GitHub issue draft. /help shows these commands.", nil
	}
	if c.Command == "/stop" {
		const answer = "Stop requested. Earlier actions are not undone. Processes and remote work may continue."
		return answer, m.sessions.StopChannelCommand(ctx, m.botID, targetID, c.SourceKey, answer)
	}
	// Rows saved before /clear existed hold /new.
	err := m.sessions.Clear(ctx, m.botID, targetID, "channel:"+c.SourceKey, c.Peer, "")
	if errors.Is(err, session.ErrSessionBusy) {
		return "Context not cleared. The main timeline is busy. Wait for the reply or stop it, then send /clear again.", nil
	}
	return "Context cleared.", err
}

// QueueChannelNotice records a platform acknowledgement without a model input.
func (m *Manager) QueueChannelNotice(ctx context.Context, peer, source string, identity session.ChannelContext, body string) error {
	target, err := m.sessions.Main(ctx, m.botID)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	id := "notice:" + source
	if _, err := m.q.InsertOutboxMessage(ctx, sqlc.InsertOutboxMessageParams{
		ID: id, EventID: id, BotID: m.botID, SessionID: target.ID,
		Kind: "control", Peer: peer, ChannelContext: string(encoded), Body: body,
		Status: "pending", CreatedAt: time.Now().UnixMilli(),
	}); err != nil {
		return err
	}
	m.Notify(peer)
	return nil
}

func (m *Manager) queueCommandReply(ctx context.Context, bot channel.Bot, r sqlc.ChannelCommand) error {
	now := time.Now().UnixMilli()
	if r.Answer == "" {
		return m.q.MarkChannelCommandDelivered(ctx, sqlc.MarkChannelCommandDeliveredParams{SourceKey: r.SourceKey, DeliveredAt: sql.NullInt64{Int64: now, Valid: true}})
	}
	target, err := m.sessions.Main(ctx, m.botID)
	if err != nil {
		return err
	}
	kind := "control"
	if r.Command == channel.FeedbackCommand {
		kind = "feedback"
	}
	var part channel.Part
	if kind == "feedback" {
		// The private draft is not part of Web history. Never send an extract of it.
		if parts := bot.Parts(channel.Reply{Status: "done", Answer: r.Answer}); len(parts) == 1 {
			part = parts[0]
		} else {
			part, err = finalMessage(bot, feedbackTooLong, "")
		}
	} else {
		part, err = finalMessage(bot, r.Answer, "")
	}
	if err != nil {
		return err
	}
	if policy := m.policy(r.Peer); policy != nil && kind == "control" {
		if err := policy.DecorateCommandReply(ctx, r, target, &part); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(part)
	if err != nil {
		return err
	}
	_, err = m.q.InsertOutboxMessage(ctx, sqlc.InsertOutboxMessageParams{
		ID: r.SourceKey, EventID: "control:" + r.SourceKey, BotID: m.botID, SessionID: target.ID,
		Kind: kind, Peer: r.Peer, ChannelContext: r.ChannelContext, RenderedMessage: string(encoded),
		Status: "pending", CreatedAt: now,
	})
	return err
}
