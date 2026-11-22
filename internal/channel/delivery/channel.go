// Package delivery runs channel bots and delivers durable replies.
package delivery

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
)

type runtime struct {
	ctx    context.Context
	bot    channel.Bot
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{}
	status channel.Status
}

// Manager runs bots and sends replies from durable storage.
type Manager struct {
	ctx       context.Context
	q         *sqlc.Queries
	db        *sql.DB
	chats     *chat.Service
	agentID   string
	setMu     sync.Mutex
	receiveMu sync.Mutex // command receipts; per peer if throughput matters
	mu        sync.Mutex
	bots      map[string]*runtime
}

// New creates a manager. Close it before closing the database.
func New(ctx context.Context, db *sql.DB, chats *chat.Service, agentID string) *Manager {
	return &Manager{ctx: ctx, db: db, q: sqlc.New(db), chats: chats, agentID: agentID, bots: make(map[string]*runtime)}
}

// Set replaces one platform's bot. Callers must not call it from channel.Bot.Run or channel.Bot.Send.
func (m *Manager) Set(name string, bot channel.Bot) {
	m.setMu.Lock()
	defer m.setMu.Unlock()
	m.mu.Lock()
	old := m.bots[name]
	delete(m.bots, name)
	m.mu.Unlock()
	if old != nil {
		old.cancel()
		<-old.done
	}
	if bot == nil {
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)
	r := &runtime{ctx: ctx, bot: bot, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), status: channel.Status{State: "running"}}
	m.mu.Lock()
	m.bots[name] = r
	m.mu.Unlock()
	go func() {
		defer close(r.done)
		var wg sync.WaitGroup
		wg.Go(func() { m.deliver(ctx, name, r) })
		err := bot.Run(ctx)
		if err != nil && ctx.Err() == nil {
			m.Report(name, channel.Status{State: "error", Reason: err.Error()})
		}
		cancel()
		wg.Wait()
	}()
}

// Close stops all bots and waits for them.
func (m *Manager) Close() {
	m.mu.Lock()
	names := make([]string, 0, len(m.bots))
	for name := range m.bots {
		names = append(names, name)
	}
	m.mu.Unlock()
	for _, name := range names {
		m.Set(name, nil)
	}
}

// Report sets runtime status. Platforms must remove credentials from error reasons.
func (m *Manager) Report(name string, status channel.Status) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.bots[name]; r != nil {
		r.status = status
	}
}

// Status returns off if no bot is registered.
func (m *Manager) Status(name string) channel.Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.bots[name]; r != nil {
		return r.status
	}
	return channel.Status{State: "off"}
}

// Notify wakes a reply's origin. Stopping main wakes all senders for withdrawn input.
func (m *Manager) Notify(key string) {
	name, _, _ := strings.Cut(key, ":")
	m.mu.Lock()
	defer m.mu.Unlock()
	for platform, r := range m.bots {
		if key != chat.MainKey && platform != name {
			continue
		}
		if media, ok := r.bot.(TelegramHooks); ok {
			media.WakeMedia()
		}
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}

// SetTelegramRetryAt persists a FloodWait retry deadline for Telegram polling.
func (m *Manager) SetTelegramRetryAt(ctx context.Context, token string, until int64) error {
	return m.q.SetTelegramRetryAt(ctx, sqlc.SetTelegramRetryAtParams{AgentID: m.agentID, Token: token, RetryAt: until})
}

// ReceivedSource reports stored platform input, including input received by a previous agent.
func (m *Manager) ReceivedSource(ctx context.Context, source string) (bool, error) {
	return m.chats.ReceivedSource(ctx, source)
}

// Source returns the stored input and preview state for this manager's agent.
func (m *Manager) Source(ctx context.Context, source string) (chat.SourceReceipt, error) {
	return m.chats.LookupSource(ctx, m.agentID, source)
}

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
	group := strings.HasPrefix(peer, "telegram:-")
	if group || msg.ChannelContext.ChatID != 0 || msg.ChannelContext.TopicID != 0 || msg.ChannelContext.TopicShared {
		allowed, err := m.TelegramInputAllowed(ctx, peer, msg.ChannelContext)
		if err != nil {
			return err
		}
		if !allowed {
			return chat.ErrGroupUnauthorized
		}
	}
	if !strings.Contains(peer, ":") || strings.Contains(peer, "/") || source == "" {
		return errors.New("invalid channel peer or source")
	}
	if len(msg.Attachments) > 10 {
		return errors.New("a message can have at most 10 attachments")
	}
	m.receiveMu.Lock()
	defer m.receiveMu.Unlock()
	if received, err := m.ReceivedSource(ctx, source); err != nil {
		return err
	} else if received {
		return nil
	}
	command, err := m.q.GetChannelCommand(ctx, source)
	if err == nil {
		if group {
			identity, encodeErr := json.Marshal(msg.ChannelContext)
			if encodeErr != nil {
				return encodeErr
			}
			if command.Peer != peer || command.ChannelContext != string(identity) {
				return chat.ErrGroupUnauthorized
			}
		}
		return m.complete(ctx, command)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	cmd := strings.TrimSpace(msg.Content)
	basicCommand := cmd == "/clear" || cmd == "/new" || cmd == "/stop" || cmd == "/help"
	if !channel.IsCommand(cmd) || !strings.HasPrefix(peer, "telegram:") && !basicCommand || msg.AlbumSource != "" || len(msg.Attachments) > 0 {
		return m.chats.SubmitMessage(ctx, m.agentID, peer, source, msg)
	}
	key := chat.MainKey
	if group {
		target, err := m.chats.GroupConversation(ctx, m.agentID, peer, msg.ChannelContext)
		if err != nil {
			return err
		}
		key = target.ChannelKey
	}
	identity, err := json.Marshal(msg.ChannelContext)
	if err != nil {
		return err
	}
	if err = m.q.CreateChannelCommand(ctx, sqlc.CreateChannelCommandParams{AgentID: m.agentID, SourceKey: source, Peer: peer, ChannelKey: key, Command: cmd, ChannelContext: string(identity)}); err != nil {
		return err
	}
	command, err = m.q.GetChannelCommand(ctx, source)
	if err != nil {
		return err
	}
	return m.complete(ctx, command)
}

func (m *Manager) complete(ctx context.Context, c sqlc.ChannelCommand) error {
	if c.AgentID != m.agentID || c.Completed != 0 {
		return nil
	}
	group := strings.HasPrefix(c.Peer, "telegram:-")
	var identity chat.ChannelContext
	var target chat.Chat
	if strings.HasPrefix(c.Peer, "telegram:") {
		var done bool
		var err error
		target, identity, done, err = m.completeTelegram(ctx, c)
		if err != nil || done {
			return err
		}
	}
	answer := "/clear clears the main timeline's context. It takes no text. /new does the same. /stop stops the main answer and its background work. /help shows these commands."
	if group {
		answer = "/clear and /new clear only this group sender session or this topic. /stop stops only its work. Private bot management, files and routines are not available."
	}
	switch c.Command {
	// Rows saved before /clear existed hold /new.
	case "/clear", "/new":
		var err error
		if group {
			err = m.chats.ClearGroup(ctx, m.agentID, target.ID, "channel:"+c.SourceKey, c.Peer, identity)
		} else {
			main, loadErr := m.chats.Main(ctx, m.agentID)
			if loadErr != nil {
				return loadErr
			}
			err = m.chats.Clear(ctx, m.agentID, main.ID, "channel:"+c.SourceKey, c.Peer, "")
		}
		switch {
		case errors.Is(err, chat.ErrGroupUnauthorized):
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
			if !group {
				var err error
				target, err = m.chats.Main(ctx, m.agentID)
				if err != nil {
					return err
				}
			}
			answer = "Stop requested. Earlier actions are not undone. Processes and remote work may continue."
			if err := m.chats.StopChannelCommand(ctx, m.agentID, target.ID, c.SourceKey, answer); err != nil {
				if errors.Is(err, chat.ErrGroupUnauthorized) {
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

// Pending reports input whose answer returns to this peer.
func (m *Manager) Pending(ctx context.Context, peer string) (bool, error) {
	n, err := m.q.CountChannelPending(ctx, sqlc.CountChannelPendingParams{AgentID: m.agentID, Peer: peer})
	return n > 0, err
}

func (m *Manager) deliver(ctx context.Context, name string, r *runtime) {
	delay := 5 * time.Second
	for {
		err := m.flush(ctx, name, r.bot)
		if err != nil && ctx.Err() == nil {
			slog.Warn("channel: reply delivery waits for retry", "channel", name, "err", err)
		}
		if err == nil {
			delay = 5 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-time.After(delay):
		}
		if err != nil {
			delay = min(delay*2, 60*time.Second)
		}
	}
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

// sendCommandReply skips delivery if the Telegram actor is no longer authorized.
// The sender closes the command receipt in both the sent and skipped cases.
func (m *Manager) sendCommandReply(ctx context.Context, bot channel.Bot, r sqlc.ChannelCommand) error {
	telegram := strings.HasPrefix(r.Peer, "telegram:")
	var identity chat.ChannelContext
	if telegram {
		if err := json.Unmarshal([]byte(r.ChannelContext), &identity); err != nil {
			return err
		}
		allowed, err := m.TelegramInputAllowed(ctx, r.Peer, identity)
		if err != nil || !allowed {
			return err
		}
	}
	parts := bot.Parts(channel.Reply{Status: "done", Answer: r.Answer})
	if r.RenderedReply != "" {
		if err := json.Unmarshal([]byte(r.RenderedReply), &parts); err != nil {
			return err
		}
	} else if telegram && r.Answer != "" {
		target, err := m.chats.ChannelConversation(ctx, m.agentID, r.Peer, identity)
		if err != nil {
			return err
		}
		actions, err := m.menuActions(ctx, r.Command, target)
		if err != nil {
			return err
		}
		parts, err = m.addButtons(ctx, parts, r.SourceKey, r.Peer, identity, target, actions)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(parts)
		if err != nil {
			return err
		}
		if err := m.q.FreezeTelegramCommandReply(ctx, sqlc.FreezeTelegramCommandReplyParams{SourceKey: r.SourceKey, RenderedReply: string(encoded)}); err != nil {
			return err
		}
	}
	for i, part := range parts {
		if _, err := m.sendPart(ctx, bot, r.Peer, r.SourceKey, i, part); err != nil {
			return fmt.Errorf("send command reply: %w", err)
		}
	}
	return nil
}

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
