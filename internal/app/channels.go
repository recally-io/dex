package app

import (
	"context"
	"database/sql"
	"net/http"
	"sync"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/channel/delivery"
	telegrampolicy "github.com/recally-io/dex/internal/channel/telegram/policy"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
)

// botChannels wires the Telegram adapter's inbound interface to the manager
// and the Telegram policy. Feishu and WeChat only use the manager.
type botChannels struct {
	*delivery.Manager
	*telegrampolicy.Policy
}

func newBotChannels(ctx context.Context, db *sql.DB, sessions *session.Service, botID string) *botChannels {
	policy := telegrampolicy.New(db, sessions, botID)
	manager := delivery.New(ctx, db, sessions, botID, map[string]delivery.Policy{"telegram": policy})
	return &botChannels{Manager: manager, Policy: policy}
}

func (b *botChannels) ClaimTelegramCallback(ctx context.Context, id, peer, remote string, identity session.ChannelContext) (bool, error) {
	return b.Policy.ClaimTelegramCallback(ctx, id, peer, remote, identity, b.CompleteCommand)
}

type channelRegistry struct {
	mu       sync.Mutex
	ctx      context.Context
	db       *sql.DB
	sessions *session.Service
	managers map[string]*botChannels
}

func (r *channelRegistry) Bot(id string) *botChannels {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.managers[id] == nil {
		r.managers[id] = newBotChannels(r.ctx, r.db, r.sessions, id)
	}
	return r.managers[id]
}

func (r *channelRegistry) webhook(w http.ResponseWriter, req *http.Request) {
	platform := req.PathValue("platform")
	if platform != "telegram" && platform != "feishu" {
		http.NotFound(w, req)
		return
	}
	r.mu.Lock()
	manager := r.managers[req.PathValue("botId")]
	r.mu.Unlock()
	if manager == nil {
		http.NotFound(w, req)
		return
	}
	manager.Webhook(w, req, platform)
}

func (r *channelRegistry) Status(botID, platform string) channel.Status {
	r.mu.Lock()
	m := r.managers[botID]
	r.mu.Unlock()
	if m == nil {
		return channel.Status{State: "off"}
	}
	return m.Status(platform)
}

func (r *channelRegistry) reactionIntake(botID, peer, id string, source session.ChannelContext) {
	r.mu.Lock()
	m := r.managers[botID]
	r.mu.Unlock()
	if m != nil {
		m.TrackReaction(peer, id, source)
	}
}

func (r *channelRegistry) reactionAdmission(botID, peer, id string, sub *engine.Submission) {
	r.mu.Lock()
	m := r.managers[botID]
	r.mu.Unlock()
	if m != nil {
		m.ReactionAdmission(peer, id, sub)
	}
}

func (r *channelRegistry) Notify(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Global fan-out; route by bot if the number of bots makes wakeups costly.
	// Each manager filters durable replies and commands by bot.
	for _, manager := range r.managers {
		manager.Notify(key)
	}
}

func (r *channelRegistry) Close() {
	r.mu.Lock()
	managers := make([]*botChannels, 0, len(r.managers))
	for _, manager := range r.managers {
		managers = append(managers, manager)
	}
	r.mu.Unlock()
	// Bots can notify the registry while they finish an in-flight message.
	for _, manager := range managers {
		manager.Close()
	}
}
