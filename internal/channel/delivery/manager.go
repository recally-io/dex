// Package delivery runs channel bots and delivers durable replies.
// Delivery is at least once: a crash after the platform accepts a part and
// before dex records confirmation can cause the part to be sent again.
package delivery

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db/sqlc"
)

type runtime struct {
	ctx             context.Context
	bot             channel.Bot
	cancel          context.CancelFunc
	done            chan struct{}
	wake            chan struct{}
	status          channel.Status
	reactions       map[string]*reactionWatch // guarded by Manager.mu
	reactionWorkers sync.WaitGroup
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

// Set stops the old bot and waits for it before starting its replacement.
// A nil bot turns the platform off. Do not call Set from Bot.Run or Bot.Send.
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
	r := &runtime{ctx: ctx, bot: bot, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), reactions: make(map[string]*reactionWatch), status: channel.Status{State: "running"}}
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
		// Cancel under the same lock that admits reaction workers, then join them.
		m.mu.Lock()
		cancel()
		m.mu.Unlock()
		wg.Wait()
		r.reactionWorkers.Wait()
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
