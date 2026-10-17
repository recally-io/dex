package app

import (
	"context"
	"database/sql"
	"sync"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
)

type channelRegistry struct {
	mu       sync.Mutex
	ctx      context.Context
	db       *sql.DB
	chats    *chat.Service
	managers map[string]*channel.Manager
}

func (r *channelRegistry) Agent(id string) *channel.Manager {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.managers[id] == nil {
		r.managers[id] = channel.New(r.ctx, r.db, r.chats, id)
	}
	return r.managers[id]
}

func (r *channelRegistry) Status(agentID, platform string) channel.Status {
	return r.Agent(agentID).Status(platform)
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
	managers := make([]*channel.Manager, 0, len(r.managers))
	for _, manager := range r.managers {
		managers = append(managers, manager)
	}
	r.mu.Unlock()
	// Bots can notify the registry while they finish an in-flight message.
	for _, manager := range managers {
		manager.Close()
	}
}
