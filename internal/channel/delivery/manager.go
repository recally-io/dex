// Package delivery runs channel bots and delivers durable replies.
// Delivery is at least once: a crash after the platform accepts a part and
// before dex records confirmation can cause the part to be sent again.
package delivery

import (
	"context"
	"database/sql"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/session"
)

type runtime struct {
	ctx             context.Context
	bot             channel.Bot
	cancel          context.CancelFunc
	done            chan struct{}
	wake            chan struct{}
	inboundWake     chan struct{}
	webhook         *channel.Webhook
	status          channel.Status
	reactions       map[string]*reactionWatch // guarded by Manager.mu
	reactionWorkers sync.WaitGroup
}

// PairingCheck checks an account binding before a shared channel command.
type PairingCheck func(ctx context.Context, q *sqlc.Queries, botID, peer, accountID string) error

// Manager runs bots and sends replies from durable storage.
type Manager struct {
	ctx      context.Context
	q        *sqlc.Queries
	db       *sql.DB
	sessions *session.Service
	botID    string
	setMu    sync.Mutex
	intakeMu sync.Mutex // Orders callback commits with runtime removal.
	mu       sync.Mutex
	commands map[string]chan struct{} // source key to active command owner
	bots     map[string]*runtime
	// These bindings do not change after New.
	pairingCheck    PairingCheck
	policies        map[string]Policy
	reactionAllowed map[string]ReactionAuthorizer
}

// New creates a manager with optional policies and reaction authorizers keyed by platform.
// Close it before closing the database.
func New(ctx context.Context, db *sql.DB, sessions *session.Service, botID string, pairingCheck PairingCheck, policies map[string]Policy, reactionAllowed map[string]ReactionAuthorizer) *Manager {
	return &Manager{
		ctx: ctx, db: db, q: sqlc.New(db), sessions: sessions, botID: botID,
		commands: make(map[string]chan struct{}), bots: make(map[string]*runtime),
		pairingCheck: pairingCheck, policies: maps.Clone(policies), reactionAllowed: maps.Clone(reactionAllowed),
	}
}

// Set stops the old bot and waits for it before starting its replacement.
// A nil bot turns the platform off. Do not call Set from Bot.Run or Bot.Send.
func (m *Manager) Set(name string, bot channel.Bot) {
	m.setMu.Lock()
	defer m.setMu.Unlock()
	m.intakeMu.Lock()
	m.mu.Lock()
	old := m.bots[name]
	delete(m.bots, name)
	m.mu.Unlock()
	m.intakeMu.Unlock()
	if old != nil {
		old.cancel()
		<-old.done
	}
	if bot == nil {
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)
	r := &runtime{ctx: ctx, bot: bot, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), inboundWake: make(chan struct{}, 1), reactions: make(map[string]*reactionWatch), status: channel.Status{State: "running"}}
	if receiver, ok := bot.(channel.WebhookReceiver); ok {
		r.webhook = receiver.InboundWebhook()
	}
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
		if key != session.MainKey && platform != name {
			continue
		}
		if inbound, ok := r.bot.(channel.InboundWaker); ok {
			inbound.WakeInbound()
		}
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}

// ReceivedSource reports stored platform input, including input received by a previous bot.
func (m *Manager) ReceivedSource(ctx context.Context, source string) (bool, error) {
	return m.sessions.ReceivedSource(ctx, source)
}

// Source returns the stored input and typing state for this manager's bot.
func (m *Manager) Source(ctx context.Context, source string) (session.SourceReceipt, error) {
	return m.sessions.LookupSource(ctx, m.botID, source)
}

// Pending reports input whose answer returns to this peer.
func (m *Manager) Pending(ctx context.Context, peer string) (bool, error) {
	n, err := m.q.CountChannelPending(ctx, sqlc.CountChannelPendingParams{BotID: m.botID, Peer: peer})
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
