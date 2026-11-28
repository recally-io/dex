package app

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"sync"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/channel/delivery"
	"github.com/recally-io/dex/internal/channel/feishu"
	"github.com/recally-io/dex/internal/channel/pairing"
	telegrampolicy "github.com/recally-io/dex/internal/channel/telegram/policy"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
)

func bindSessionChannels(sessions *session.Service) {
	sessions.ConfigureChannelPolicy(session.ChannelPolicy{
		CheckPeer: func(peer string) error {
			if strings.HasPrefix(peer, "telegram:") {
				return telegrampolicy.CheckPeer(peer)
			}
			if !channelRoute(peer).User {
				return session.ErrChannelUnauthorized
			}
			return nil
		},
		CheckInputIn: func(ctx context.Context, q *sqlc.Queries, botID, peer string, source session.ChannelContext) error {
			if strings.HasPrefix(peer, "telegram:") {
				return telegrampolicy.CheckInputActorIn(ctx, q, botID, peer, source)
			}
			return pairing.RequirePairedChannel(ctx, q, botID, peer, source.AccountID)
		},
		CheckCommandIn: telegrampolicy.CheckCommandIn,
		Route:          channelRoute,
	})
	sessions.ConfigureChannelStaging(telegrampolicy.LookupStagedSourceIn, telegrampolicy.CheckInputIn, telegrampolicy.DiscardStagedIn)
}

func channelRoute(peer string) session.ChannelRoute {
	platform, _, ok := strings.Cut(peer, ":")
	if !ok {
		return session.ChannelRoute{}
	}
	switch platform {
	case "telegram":
		return session.ChannelRoute{User: true, Owner: telegrampolicy.PrivatePeer(peer), Quote: true, Files: true}
	case "feishu":
		return session.ChannelRoute{User: true, Owner: true, Quote: true}
	case "wechat":
		return session.ChannelRoute{User: true, Owner: true}
	default:
		return session.ChannelRoute{}
	}
}

// botChannels wires the Telegram adapter's inbound interface to the manager
// and the Telegram policy. Feishu and WeChat only use the manager.
type botChannels struct {
	*delivery.Manager
	*telegrampolicy.Policy
}

func newBotChannels(ctx context.Context, db *sql.DB, sessions *session.Service, botID string) *botChannels {
	policy := telegrampolicy.New(db, sessions, botID)
	q := sqlc.New(db)
	manager := delivery.New(ctx, db, sessions, botID, pairing.RequirePairedChannel, map[string]delivery.Policy{"telegram": policy}, map[string]delivery.ReactionAuthorizer{
		"telegram": policy.ReactionAllowed,
		"feishu": func(ctx context.Context, peer string, source session.ChannelContext) bool {
			return feishu.ReactionAllowed(ctx, q, botID, peer, source)
		},
	})
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

func (r *channelRegistry) RunningCount(botID string) int64 {
	var count int64
	for _, platform := range []string{"telegram", "wechat", "feishu"} {
		if r.Status(botID, platform).State == "running" {
			count++
		}
	}
	return count
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
