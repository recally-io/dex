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
	"strings"
	"sync"
	"time"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
)

// ErrPermanent marks a part the platform cannot accept. The sender logs and skips it.
// Other errors keep the part for retry.
var ErrPermanent = errors.New("permanent channel delivery failure")

// Status is the public runtime state. Reason must contain no credentials.
type Status struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// Bot is a transport. Run and Send can execute at the same time.
type Bot interface {
	Run(context.Context) error
	Parts(chat.Reply) []Part
	// Replies use the submission ID; commands use the source key.
	// Part is zero-based. The receipt contains every remote message ID for that part.
	Send(ctx context.Context, peer, deliveryID string, part int, content Part) (Receipt, error)
}

type runtime struct {
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
	r := &runtime{bot: bot, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), status: Status{State: "running"}}
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
		return m.complete(ctx, command)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	cmd := strings.TrimSpace(msg.Content)
	if !IsCommand(cmd) {
		return m.chats.SubmitMessage(ctx, m.agentID, peer, source, msg)
	}
	if err = m.q.CreateChannelCommand(ctx, sqlc.CreateChannelCommandParams{AgentID: m.agentID, SourceKey: source, Peer: peer, ChannelKey: chat.MainKey, Command: cmd}); err != nil {
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
	case "/clear", "/new", "/stop", "/help":
		return true
	}
	return false
}

func (m *Manager) complete(ctx context.Context, c sqlc.ChannelCommand) error {
	if c.AgentID != m.agentID || c.Completed != 0 {
		return nil
	}
	answer := "/clear clears the main timeline's context. It takes no text. /new does the same. /stop stops the main answer and its background work. /help shows these commands."
	switch {
	case c.ChannelKey != chat.MainKey:
		// A saved peer command cannot acquire main's scope after an upgrade.
		answer = "This command is from an earlier version. Send a new command if needed."
	// Rows saved before /clear existed hold /new.
	case c.Command == "/clear" || c.Command == "/new":
		main, err := m.chats.Main(ctx, m.agentID)
		if err != nil {
			return err
		}
		// A stable reset ID closes the crash window before command completion.
		err = m.chats.Clear(ctx, m.agentID, main.ID, "channel:"+c.SourceKey, c.Peer, "")
		switch {
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
			main, err := m.chats.Main(ctx, m.agentID)
			if err != nil {
				return err
			}
			answer = "Stop requested. Earlier actions are not undone. Processes and remote work may continue."
			if err := m.chats.StopChannelCommand(ctx, m.agentID, main.ID, c.SourceKey, answer); err != nil {
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
			receipt, err := m.sendPart(ctx, bot, r.Origin, r.ID, i, parts[i])
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
			if err := m.confirmPart(ctx, r, receipt, &remotes); err != nil {
				return err
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
		parts := bot.Parts(chat.Reply{Status: "done", Answer: r.Answer})
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
