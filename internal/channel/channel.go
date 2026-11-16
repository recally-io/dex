// Package channel owns commands, bot lifetime, and reply delivery.
//
// New connects a Manager to control's database and chat service. A platform passes inbound
// messages to Receive. Peer names have the form "platform:remote-id" and
// contain no slash. Source keys identify updates globally, including the bot identity.
//
// A platform implements Bot and registers it with Set("platform", bot). Set stops and waits for
// the previous bot before it starts the replacement; nil turns it off. Run receives messages.
// Parts renders and splits a final reply. Send confirms one part with its stable identity.
// ErrPermanent skips a rejected part. Report updates
// runtime status; Status returns it for the settings API. Connect chat's notify to Notify.
//
// Receive sends channel input to main. /clear clears main's context, and /new is its old name;
// /stop stops main's work.
// Replies use the input's origin, including results from background work.
// Pending reports whether the peer has work, for typing indicators. Delivery retries after a
// failed send and after restart. It is at least once: a crash after the remote service accepts
// a part but before control records success can send that part again.
package channel

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

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

// ErrPermanent marks a part the platform cannot accept. The sender logs and skips it.
// Other errors keep the part for retry.
var ErrPermanent = errors.New("permanent channel delivery failure")

// ErrGroupRejected permits a durable switch to single-file delivery after a definitive rejection.
var ErrGroupRejected = errors.New("media group was rejected")

// Status is the public runtime state. Reason must contain no credentials.
type Status struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// Bot is one platform transport. The application creates concrete bots and
// registers them with Manager.Set; this package never imports an adapter.
//
// Run and Send can execute at the same time, and Send can run while Run is
// receiving. When ctx is cancelled, each method must stop and join the
// goroutines, connections, and timers that it started before it returns.
//
// A bot can also implement GroupParts, MediaWaker, Finisher, or Controller.
// They are optional; today only Telegram implements them.
type Bot interface {
	// Run receives platform input until ctx ends or the transport fails.
	Run(context.Context) error
	// Parts renders and splits a final reply for this platform. Each Text is
	// already in the platform's own format. Manager stores a reply's parts
	// before its first Send, so a retry sends the same parts.
	Parts(chat.Reply) []Part
	// Send delivers one part. Replies use the submission ID; commands use the
	// source key. Part is zero-based. The receipt contains every remote
	// message ID for that part. ErrPermanent rejects the part; other errors keep
	// it for retry. A Part variant that this adapter cannot deliver is the
	// adapter's own decision; it does not change what other platforms support.
	Send(ctx context.Context, peer, deliveryID string, part int, content Part) (Receipt, error)
}

// GroupParts plans native media groups for a reply before Manager freezes it.
// File bodies are loaded for planning. Manager never calls it for a frozen or
// partly delivered reply.
type GroupParts interface {
	GroupParts([]Part) []Part
}

// MediaWaker wakes inbound media collection after Notify.
type MediaWaker interface {
	WakeMedia()
}

// Finisher runs after every part of a reply is sent and before Manager marks
// the reply delivered. An error keeps the reply for retry, so Finish must
// tolerate a repeated call.
type Finisher interface {
	Finish(ctx context.Context, peer string, source chat.ChannelContext, status string) error
}

// Controller performs one agent-requested action on a confirmed remote message.
type Controller interface {
	Control(ctx context.Context, peer, remote string, args engine.TelegramRequest) (Receipt, error)
}

type runtime struct {
	ctx    context.Context
	bot    Bot
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{}
	status Status
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

// Set replaces one platform's bot. Callers must not call it from Bot.Run or Bot.Send.
func (m *Manager) Set(name string, bot Bot) {
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
	r := &runtime{ctx: ctx, bot: bot, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), status: Status{State: "running"}}
	m.mu.Lock()
	m.bots[name] = r
	m.mu.Unlock()
	go func() {
		defer close(r.done)
		var wg sync.WaitGroup
		wg.Go(func() { m.deliver(ctx, name, r) })
		err := bot.Run(ctx)
		if err != nil && ctx.Err() == nil {
			m.Report(name, Status{State: "error", Reason: err.Error()})
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
func (m *Manager) Report(name string, status Status) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.bots[name]; r != nil {
		r.status = status
	}
}

// Status returns off if no bot is registered.
func (m *Manager) Status(name string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.bots[name]; r != nil {
		return r.status
	}
	return Status{State: "off"}
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
		if media, ok := r.bot.(MediaWaker); ok {
			media.WakeMedia()
		}
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}

// ReceivedSource reports stored platform input, including input received by a previous agent.
func (m *Manager) ReceivedSource(ctx context.Context, source string) (bool, error) {
	return m.chats.ReceivedSource(ctx, source)
}

// Source returns the stored input and preview state for this manager's agent.
func (m *Manager) Source(ctx context.Context, source string) (chat.SourceReceipt, error) {
	return m.chats.LookupSource(ctx, m.agentID, source)
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
	if !IsCommand(cmd) || !strings.HasPrefix(peer, "telegram:") && !basicCommand || msg.AlbumSource != "" || len(msg.Attachments) > 0 {
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

// IsCommand reports whether trimmed channel text is a command rather than input for the model.
func IsCommand(text string) bool {
	switch text {
	case "/clear", "/new", "/stop", "/help", "/menu", "/status", "/settings", "/sessions", "/workflows", "/question":
		return true
	}
	return strings.HasPrefix(text, "/run ") || strings.HasPrefix(text, "/answer ")
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
	switch {
	case c.ChannelKey != chat.MainKey && !group:
		// A saved peer command cannot acquire main's scope after an upgrade.
		answer = "This command is from an earlier version. Send a new command if needed."
	// Rows saved before /clear existed hold /new.
	case c.Command == "/clear" || c.Command == "/new":
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
	case c.Command == "/stop":
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

func (m *Manager) sendPart(ctx context.Context, bot Bot, peer, id string, index int, part Part) (Receipt, error) {
	var err error
	if part.File != nil {
		file := *part.File
		part.File = &file
		part.File.Body, err = m.q.GetChannelReplyFile(ctx, sqlc.GetChannelReplyFileParams{OperationKey: part.File.Key, SubmissionID: id})
		if errors.Is(err, sql.ErrNoRows) {
			err = fmt.Errorf("%w: file snapshot is no longer available", ErrPermanent)
		}
	}
	if len(part.Group) > 0 {
		files := slices.Clone(part.Group)
		for i := range files {
			files[i].Body, err = m.q.GetChannelReplyFile(ctx, sqlc.GetChannelReplyFileParams{OperationKey: files[i].Key, SubmissionID: id})
			if errors.Is(err, sql.ErrNoRows) {
				err = fmt.Errorf("%w: group snapshot is no longer available", ErrPermanent)
			}
			if err != nil {
				break
			}
		}
		part.Group = files
	}
	var receipt Receipt
	if err == nil {
		receipt, err = bot.Send(ctx, peer, id, index, part)
	}
	if errors.Is(err, ErrPermanent) {
		slog.Warn("channel: skipped a rejected reply part", "delivery", id, "part", index, "err", err)
		if stored := m.q.SetChannelReplyError(ctx, sqlc.SetChannelReplyErrorParams{ID: id, Reason: err.Error()}); stored != nil {
			return Receipt{}, stored
		}
		return Receipt{}, nil
	}
	if err == nil && len(part.Buttons) > 0 && len(receipt.MessageIDs) == 1 {
		err = m.q.BindTelegramCallbackMessage(ctx, sqlc.BindTelegramCallbackMessageParams{SourceID: id, Part: int64(index), MessageID: receipt.MessageIDs[0]})
	}
	return receipt, err
}

func (m *Manager) flush(ctx context.Context, name string, bot Bot) error {
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
	replies, err := m.q.ListChannelReplies(ctx, sqlc.ListChannelRepliesParams{AgentID: m.agentID, OriginStart: name + ":", OriginEnd: name + ";"})
	if err != nil {
		return err
	}
	blocked := make(map[string]bool)
	for _, r := range replies {
		if blocked[r.Origin] {
			continue
		}
		if r.ChannelRetryAt > time.Now().UnixMilli() && r.ChannelRevoked == 0 {
			blocked[r.Origin] = true
			continue
		}
		parts, err := m.replyParts(ctx, bot, r)
		if err != nil {
			return err
		}
		var remotes []string
		if err := json.Unmarshal([]byte(r.ChannelRemoteIds), &remotes); err != nil {
			return err
		}
		for i := int(r.DeliveredParts); i < len(parts); i++ {
			allowed, err := m.authorizedReply(ctx, name, r)
			if err != nil {
				return err
			}
			if !allowed {
				if err := m.q.SetChannelReplyError(ctx, sqlc.SetChannelReplyErrorParams{ID: r.ID, Reason: "destination no longer authorized"}); err != nil {
					return err
				}
				break
			}
			err = m.sendReplyPart(ctx, name, bot, r, i, parts[i], &remotes)
			if err != nil {
				retryAt := time.Now().Add(5 * time.Second).UnixMilli()
				if flood, ok := errors.AsType[*RetryError](err); ok {
					retryAt = time.Now().Add(flood.After).UnixMilli()
				}
				if stored := m.q.SetChannelReplyError(ctx, sqlc.SetChannelReplyErrorParams{ID: r.ID, RetryAt: retryAt, Reason: err.Error()}); stored != nil {
					return errors.Join(err, stored)
				}
				return err
			}
			r.ChannelGroupFallback, r.ChannelGroupCursor = 0, 0
		}
		if finished, ok := bot.(Finisher); ok {
			allowed, err := m.authorizedReply(ctx, name, r)
			if err != nil {
				return err
			}
			if allowed {
				var source chat.ChannelContext
				if err := json.Unmarshal([]byte(r.ChannelContext), &source); err != nil {
					return err
				}
				if err := finished.Finish(ctx, r.Origin, source, r.Status); err != nil {
					if saved := m.q.SetChannelReplyError(ctx, sqlc.SetChannelReplyErrorParams{ID: r.ID, RetryAt: time.Now().Add(5 * time.Second).UnixMilli(), Reason: err.Error()}); saved != nil {
						return errors.Join(err, saved)
					}
					return err
				}
			}
		}
		if err := m.q.MarkChannelReplyDelivered(ctx, sqlc.MarkChannelReplyDeliveredParams{ID: r.ID, DeliveredAt: sql.NullInt64{Int64: time.Now().UnixMilli(), Valid: true}}); err != nil {
			return err
		}
	}
	acks, err := m.q.ListChannelCommandReplies(ctx, sqlc.ListChannelCommandRepliesParams{AgentID: m.agentID, Prefix: sql.NullString{String: name + ":", Valid: true}})
	if err != nil {
		return err
	}
	for _, r := range acks {
		if strings.HasPrefix(r.Peer, "telegram:") {
			var source chat.ChannelContext
			if err := json.Unmarshal([]byte(r.ChannelContext), &source); err != nil {
				return err
			}
			allowed, err := m.TelegramInputAllowed(ctx, r.Peer, source)
			if err != nil {
				return err
			}
			if !allowed {
				if err := m.q.MarkChannelCommandDelivered(ctx, sqlc.MarkChannelCommandDeliveredParams{SourceKey: r.SourceKey, DeliveredAt: sql.NullInt64{Int64: time.Now().UnixMilli(), Valid: true}}); err != nil {
					return err
				}
				continue
			}
		}
		parts := bot.Parts(chat.Reply{Status: "done", Answer: r.Answer})
		if r.RenderedReply != "" {
			if err := json.Unmarshal([]byte(r.RenderedReply), &parts); err != nil {
				return err
			}
		} else if strings.HasPrefix(r.Peer, "telegram:") && r.Answer != "" {
			var identity chat.ChannelContext
			if err := json.Unmarshal([]byte(r.ChannelContext), &identity); err != nil {
				return err
			}
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
			if _, err = m.sendPart(ctx, bot, r.Peer, r.SourceKey, i, part); err != nil {
				return fmt.Errorf("send command reply: %w", err)
			}
		}
		if err = m.q.MarkChannelCommandDelivered(ctx, sqlc.MarkChannelCommandDeliveredParams{SourceKey: r.SourceKey, DeliveredAt: sql.NullInt64{Int64: time.Now().UnixMilli(), Valid: true}}); err != nil {
			return err
		}
	}
	return m.flushNotifications(ctx, name, bot)
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
