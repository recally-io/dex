package delivery

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/feedback"
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
func (m *Manager) Receive(ctx context.Context, peer, source string, msg chat.Message) error {
	if chat.NonPrivateTelegramPeer(peer) {
		return chat.ErrTelegramUnauthorized
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
		return m.complete(ctx, source)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	cmd := strings.TrimSpace(msg.Content)
	isFeedback := channel.IsFeedbackCommand(cmd)
	basicCommand := cmd == "/clear" || cmd == "/new" || cmd == "/stop" || cmd == "/help"
	telegramCommand := strings.HasPrefix(peer, "telegram:") && channel.IsCommand(cmd)
	isCommand := isFeedback || basicCommand || telegramCommand
	if !isCommand || msg.AlbumSource != "" || len(msg.Attachments) > 0 {
		return m.chats.AcceptMessage(ctx, m.agentID, peer, source, msg)
	}
	if err := chat.RequirePairedChannel(ctx, m.q, m.agentID, peer, msg.ChannelContext); err != nil {
		return err
	}
	identity, err := json.Marshal(msg.ChannelContext)
	if err != nil {
		return err
	}
	if isFeedback {
		answer := feedbackReply(cmd)
		if err := m.q.CreateFeedbackCommand(ctx, sqlc.CreateFeedbackCommandParams{AgentID: m.agentID, SourceKey: source, Peer: peer, ChannelContext: string(identity), Answer: answer}); err != nil {
			return err
		}
		m.Notify(peer)
		return nil
	}
	if err = m.q.CreateChannelCommand(ctx, sqlc.CreateChannelCommandParams{AgentID: m.agentID, SourceKey: source, Peer: peer, ChannelKey: chat.MainKey, Command: cmd, ChannelContext: string(identity)}); err != nil {
		return err
	}
	return m.complete(ctx, source)
}

const feedbackTooLong = "This draft is too long for one channel reply. Shorten it or use Feedback in Web."

const feedbackInstructions = "Send /feedback followed by your title on the first line and your description on the lines below. " + feedback.PublicWarning

// feedbackReply builds a reply from explicitly supplied text, never from a chat session.
func feedbackReply(text string) string {
	_, rest := channel.SplitCommand(text)
	if rest == "" {
		return feedbackInstructions
	}
	title, description, _ := strings.Cut(rest, "\n")
	draft, err := feedback.Build(title, description)
	if err != nil {
		return "Draft not ready: " + err.Error() + ". " + feedbackInstructions
	}
	const fence = "```"
	if strings.Contains(draft.Title, fence) || strings.Contains(draft.Description, fence) {
		return "This draft contains code fences. Remove them or use Feedback in Web."
	}
	answer := feedback.PublicWarning + " Copy the title and description below. Submit with your own GitHub account:\n\n" + feedback.IssueURL + "\n\n" + fence + "\nTitle: " + draft.Title + "\n\nDescription:\n" + draft.Description + "\n" + fence
	// Keep the complete draft in one message.
	if len(answer) > 4000 {
		return feedbackTooLong
	}
	return answer
}

func (m *Manager) complete(ctx context.Context, source string) error {
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
	if c.AgentID != m.agentID || c.Completed != 0 || c.DeliveredAt.Valid {
		return nil
	}
	var target chat.Chat
	if strings.HasPrefix(c.Peer, "telegram:") {
		var done bool
		var err error
		target, done, err = m.completeTelegram(ctx, c)
		if err != nil || done {
			return err
		}
	}
	answer := "/clear clears the main timeline's context. It takes no text. /new does the same. /stop stops the main answer and its background work. /feedback prepares a public GitHub issue draft. /help shows these commands."
	switch c.Command {
	// Rows saved before /clear existed hold /new.
	case "/clear", "/new":
		if target.ID == 0 {
			target, err = m.chats.Main(ctx, m.agentID)
			if err != nil {
				return err
			}
		}
		err := m.chats.Clear(ctx, m.agentID, target.ID, "channel:"+c.SourceKey, c.Peer, "")
		switch {
		case errors.Is(err, chat.ErrTelegramUnauthorized):
			answer = ""
		case errors.Is(err, chat.ErrConversationBusy):
			answer = "Context not cleared. The main timeline is busy. Wait for the reply or stop it, then send /clear again."
		case err != nil:
			return err
		default:
			answer = "Context cleared."
		}
	case "/stop":
		answer = c.Answer
		if answer == "" {
			if target.ID == 0 {
				var err error
				target, err = m.chats.Main(ctx, m.agentID)
				if err != nil {
					return err
				}
			}
			answer = "Stop requested. Earlier actions are not undone. Processes and remote work may continue."
			if err := m.chats.StopChannelCommand(ctx, m.agentID, target.ID, c.SourceKey, answer); err != nil {
				if errors.Is(err, chat.ErrTelegramUnauthorized) {
					return m.q.CompleteChannelCommand(ctx, sqlc.CompleteChannelCommandParams{SourceKey: c.SourceKey, Answer: ""})
				}
				return err
			}
		}
	}
	if err := m.q.CompleteChannelCommand(ctx, sqlc.CompleteChannelCommandParams{Answer: answer, SourceKey: c.SourceKey}); err != nil {
		return err
	}
	m.Notify(c.Peer)
	return nil
}

// QueueChannelNotice records a platform acknowledgement without a model input.
func (m *Manager) QueueChannelNotice(ctx context.Context, peer, source string, identity chat.ChannelContext, body string) error {
	target, err := m.chats.Main(ctx, m.agentID)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	id := "notice:" + source
	if _, err := m.q.InsertOutboxMessage(ctx, sqlc.InsertOutboxMessageParams{
		ID: id, EventID: id, AgentID: m.agentID, ConversationID: target.ID,
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
	target, err := m.chats.Main(ctx, m.agentID)
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
	if strings.HasPrefix(r.Peer, "telegram:") && kind == "control" {
		var identity chat.ChannelContext
		if err := json.Unmarshal([]byte(r.ChannelContext), &identity); err != nil {
			return err
		}
		actions, err := m.menuActions(ctx, r.Command, target)
		if err != nil {
			return err
		}
		if err := m.addButtons(ctx, &part, r.SourceKey, r.Peer, identity, target, actions); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(part)
	if err != nil {
		return err
	}
	_, err = m.q.InsertOutboxMessage(ctx, sqlc.InsertOutboxMessageParams{
		ID: r.SourceKey, EventID: "control:" + r.SourceKey, AgentID: m.agentID, ConversationID: target.ID,
		Kind: kind, Peer: r.Peer, ChannelContext: r.ChannelContext, RenderedMessage: string(encoded),
		Status: "pending", CreatedAt: now,
	})
	return err
}
