// Package channel owns peer-to-chat mapping, commands, bot lifetime, and reply delivery.
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
// Receive handles /new, /stop, and /help before submitting messages. /new preserves old chats
// and selects a key of the form "peer/uuid". Platforms must route by peer, not by channel key.
// Pending reports whether the peer has work, for typing indicators. Delivery retries after a
// failed send and after restart. It is at least once: a crash after the remote service accepts
// a part but before control records success can send that part again.
package channel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"uuid"

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
	Parts(chat.Reply) []string
	// Replies use the submission ID; commands use the source key. Part is zero-based.
	Send(ctx context.Context, peer, deliveryID string, part int, text string) error
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
	db        *sql.DB
	q         *sqlc.Queries
	chats     *chat.Service
	agentID   string
	setMu     sync.Mutex
	receiveMu sync.Mutex // command and peer changes; per peer if throughput matters
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

// Notify wakes delivery without blocking the worker's reader. The database holds the reply.
func (m *Manager) Notify(key string) {
	name, _, _ := strings.Cut(key, ":")
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.bots[name]; r != nil {
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
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
	if _, err := m.q.GetSubmissionBySource(ctx, sql.NullString{String: source, Valid: true}); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	command, err := m.q.GetChannelCommand(ctx, source)
	if err == nil {
		return m.complete(ctx, command)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	key, err := m.q.EnsureChannelPeer(ctx, sqlc.EnsureChannelPeerParams{Peer: peer, ChannelKey: peer})
	if err != nil {
		return err
	}
	cmd := strings.TrimSpace(msg.Content)
	if cmd != "/new" && cmd != "/stop" && cmd != "/help" {
		return m.chats.SubmitMessage(ctx, m.agentID, key, source, msg)
	}
	// The command and rotation commit together. A crash cannot rotate the same command twice.
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := m.q.WithTx(tx)
	if err = q.CreateChannelCommand(ctx, sqlc.CreateChannelCommandParams{SourceKey: source, Peer: peer, ChannelKey: key, Command: cmd}); err != nil {
		return err
	}
	if cmd == "/new" {
		err = q.SetChannelPeer(ctx, sqlc.SetChannelPeerParams{Peer: peer, ChannelKey: peer + "/" + uuid.NewV7().String()})
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	command, err = m.q.GetChannelCommand(ctx, source)
	if err != nil {
		return err
	}
	return m.complete(ctx, command)
}

func (m *Manager) complete(ctx context.Context, c sqlc.ChannelCommand) error {
	if c.Completed != 0 {
		return nil
	}
	answer := "/new starts a new chat. /stop stops the current answer. /help shows these commands."
	switch c.Command {
	case "/new":
		answer = "Started a new chat."
	case "/stop":
		conv, err := m.q.GetConversationByKey(ctx, sqlc.GetConversationByKeyParams{AgentID: m.agentID, ChannelKey: c.ChannelKey})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			if err = m.chats.Abort(ctx, m.agentID, conv.ID); err != nil {
				return err
			}
		}
		answer = "Stopped the current answer."
	}
	if err := m.q.CompleteChannelCommand(ctx, sqlc.CompleteChannelCommandParams{Answer: answer, SourceKey: c.SourceKey}); err != nil {
		return err
	}
	m.Notify(c.Peer)
	return nil
}

// Pending reports work in the peer's current or previous chats.
func (m *Manager) Pending(ctx context.Context, peer string) (bool, error) {
	n, err := m.q.CountChannelPending(ctx, peer)
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

func sendPart(ctx context.Context, bot Bot, peer, id string, part int, text string) error {
	err := bot.Send(ctx, peer, id, part, text)
	if errors.Is(err, ErrPermanent) {
		slog.Warn("channel: skipped a rejected reply part", "delivery", id, "part", part, "err", err)
		return nil
	}
	return err
}

func (m *Manager) flush(ctx context.Context, name string, bot Bot) error {
	m.receiveMu.Lock()
	commands, err := m.q.ListChannelCommands(ctx)
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
	replies, err := m.q.ListChannelReplies(ctx, sql.NullString{String: name + ":", Valid: true})
	if err != nil {
		return err
	}
	for _, r := range replies {
		peer, _, _ := strings.Cut(r.ChannelKey, "/")
		parts := bot.Parts(chat.Reply{Status: r.Status, Answer: r.Answer.String, Reason: r.Reason.String})
		for i := int(r.DeliveredParts); i < len(parts); i++ {
			if err = sendPart(ctx, bot, peer, r.ID, i, parts[i]); err != nil {
				return err
			}
			if err = m.q.AdvanceChannelReply(ctx, r.ID); err != nil {
				return err
			}
		}
		if err = m.q.MarkChannelReplyDelivered(ctx, sqlc.MarkChannelReplyDeliveredParams{ID: r.ID, DeliveredAt: sql.NullInt64{Int64: time.Now().UnixMilli(), Valid: true}}); err != nil {
			return err
		}
	}
	acks, err := m.q.ListChannelCommandReplies(ctx, sql.NullString{String: name + ":", Valid: true})
	if err != nil {
		return err
	}
	for _, r := range acks {
		// Fixed command replies fit one part; add progress if commands gain long replies.
		for i, part := range bot.Parts(chat.Reply{Status: "done", Answer: r.Answer}) {
			if err = sendPart(ctx, bot, r.Peer, r.SourceKey, i, part); err != nil {
				return fmt.Errorf("send command reply: %w", err)
			}
		}
		if err = m.q.MarkChannelCommandDelivered(ctx, sqlc.MarkChannelCommandDeliveredParams{SourceKey: r.SourceKey, DeliveredAt: sql.NullInt64{Int64: time.Now().UnixMilli(), Valid: true}}); err != nil {
			return err
		}
	}
	return nil
}
