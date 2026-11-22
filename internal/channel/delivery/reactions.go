package delivery

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/engine"
)

const (
	maxReactionWatches = 64
	reactionTimeout    = 5 * time.Second
)

type reactionWatch struct {
	peer, id string
	source   chat.ChannelContext
	native   atomic.Pointer[engine.Submission]
}

// TrackReaction is called only for a new, committed input. No stored inputs or
// reaction requests are replayed on restart. Under load, drop new observations.
func (m *Manager) TrackReaction(peer, id string, source chat.ChannelContext) {
	// A correction uses an existing platform message. Do not reset its prior status.
	if source.Edited {
		return
	}
	platform, _, _ := strings.Cut(peer, ":")
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.bots[platform]
	if r == nil || r.ctx.Err() != nil || len(r.reactions) >= maxReactionWatches {
		return
	}
	bot, ok := r.bot.(channel.Reactor)
	if !ok || !bot.ReactionsEnabled(peer) {
		return
	}
	// One writer per platform message.
	for _, watch := range r.reactions {
		if watch.peer == peer && watch.source.MessageID == source.MessageID {
			return
		}
	}
	watch := &reactionWatch{peer: peer, id: id, source: source}
	r.reactions[id] = watch
	r.reactionWorkers.Go(func() {
		defer func() {
			m.mu.Lock()
			delete(r.reactions, id)
			m.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(r.ctx, 30*time.Minute)
		defer cancel()
		m.watchReaction(ctx, platform, bot, watch)
	})
}

// ReactionAdmission supplies the native handle, not a claim that this input is running.
func (m *Manager) ReactionAdmission(peer, id string, sub *engine.Submission) {
	platform, _, _ := strings.Cut(peer, ":")
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.bots[platform]; r != nil {
		if watch := r.reactions[id]; watch != nil {
			watch.native.Store(sub)
		}
	}
}

func (m *Manager) watchReaction(ctx context.Context, platform string, bot channel.Reactor, watch *reactionWatch) {
	last := channel.Reaction(0)
	previous := ""
	status := channel.ReactionQueued
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		if status > last {
			callCtx, cancel := context.WithTimeout(ctx, reactionTimeout)
			allowed := m.reactionAllowed(callCtx, platform, watch)
			var err error
			if allowed {
				previous, err = bot.React(callCtx, watch.peer, watch.source.MessageID, previous, status)
			}
			cancel()
			if !allowed || errors.Is(err, channel.ErrReactionUnknown) || status >= channel.ReactionSucceeded {
				return
			}
			last = status // Never retry a reaction, even if the platform rejected it.
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		status = m.reactionState(ctx, watch, last)
	}
}

func (m *Manager) reactionAllowed(ctx context.Context, platform string, watch *reactionWatch) bool {
	if platform == "feishu" {
		if err := db.RequireActive(ctx, m.q, m.agentID); err != nil {
			return false
		}
		cfg, err := m.q.GetFeishuSettings(ctx, m.agentID)
		return err == nil && cfg.OwnerID != "" && watch.peer == "feishu:"+cfg.OwnerID &&
			watch.source.SenderID == cfg.OwnerID && watch.source.AccountID == cfg.AppID &&
			(!cfg.Reactions.Valid || cfg.Reactions.Int64 == 1)
	}
	if strings.HasPrefix(watch.peer, "telegram:-") {
		allowed, err := m.chats.GroupReplyAllowed(ctx, m.agentID, watch.id)
		return err == nil && allowed
	}
	allowed, err := m.chats.TelegramInputAllowed(ctx, m.agentID, watch.peer, watch.source)
	return err == nil && allowed
}

func (m *Manager) reactionState(ctx context.Context, watch *reactionWatch, last channel.Reaction) channel.Reaction {
	r, err := m.q.GetChannelReactionReceipt(ctx, watch.id)
	if err != nil {
		return last
	}
	if r.Status == "pending" {
		if last < channel.ReactionProcessing {
			if sub := watch.native.Load(); sub != nil {
				if running, err := sub.Running(ctx); err == nil && running {
					return channel.ReactionProcessing
				}
			}
		}
		return last
	}
	if r.Status != "done" {
		switch r.Reason.String {
		case "aborted", "stopped", "withdrawn":
			return channel.ReactionCancelled
		default:
			return channel.ReactionFailed
		}
	}
	if r.RequiredReplyID == "" {
		if r.ExitAction == "ask_user" || r.ExitAction == "notify_user" {
			// A deferred question may still require delivery. An unknown reply is not success.
			return last
		}
		return channel.ReactionSucceeded
	}
	switch r.ReplyStatus {
	case "delivered":
		if r.SkippedPart != 0 {
			return channel.ReactionFailed
		}
		return channel.ReactionSucceeded
	case "revoked", "superseded":
		return channel.ReactionCancelled
	default:
		return last
	}
}
