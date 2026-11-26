package app

import (
	"context"
	"database/sql"
	"net/http"
	"sync"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/channel/delivery"
	"github.com/recally-io/dex/internal/channel/telegram"

	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
)

var _ delivery.TelegramHooks = (*telegram.Bot)(nil)

type channelRegistry struct {
	mu       sync.Mutex
	ctx      context.Context
	db       *sql.DB
	sessions *session.Service
	managers map[string]*delivery.Manager
}

func (r *channelRegistry) Agent(id string) *delivery.Manager {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.managers[id] == nil {
		r.managers[id] = delivery.New(r.ctx, r.db, r.sessions, id)
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
	manager := r.managers[req.PathValue("agentId")]
	r.mu.Unlock()
	if manager == nil {
		http.NotFound(w, req)
		return
	}
	manager.Webhook(w, req, platform)
}

func (r *channelRegistry) Status(agentID, platform string) channel.Status {
	r.mu.Lock()
	m := r.managers[agentID]
	r.mu.Unlock()
	if m == nil {
		return channel.Status{State: "off"}
	}
	return m.Status(platform)
}

func (r *channelRegistry) reactionIntake(agentID, peer, id string, source session.ChannelContext) {
	r.mu.Lock()
	m := r.managers[agentID]
	r.mu.Unlock()
	if m != nil {
		m.TrackReaction(peer, id, source)
	}
}

func (r *channelRegistry) reactionAdmission(agentID, peer, id string, sub *engine.Submission) {
	r.mu.Lock()
	m := r.managers[agentID]
	r.mu.Unlock()
	if m != nil {
		m.ReactionAdmission(peer, id, sub)
	}
}

func (r *channelRegistry) Notify(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Global fan-out; route by agent if the number of agents makes wakeups costly.
	// Each manager filters durable replies and commands by agent.
	for _, manager := range r.managers {
		manager.Notify(key)
	}
}

func (r *channelRegistry) Close() {
	r.mu.Lock()
	managers := make([]*delivery.Manager, 0, len(r.managers))
	for _, manager := range r.managers {
		managers = append(managers, manager)
	}
	r.mu.Unlock()
	// Bots can notify the registry while they finish an in-flight message.
	for _, manager := range managers {
		manager.Close()
	}
}
