package delivery

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/engine"
	"github.com/recally-io/dex/internal/session"
)

const (
	maxReactionWatches = 64
	reactionTimeout    = 5 * time.Second
	reactionPoll       = 150 * time.Millisecond
)

// ReactionAuthorizer checks current access before a status reaction. If the
// platform has no authorizer, the manager does not send reactions.
type ReactionAuthorizer func(ctx context.Context, peer string, source session.ChannelContext) bool

type reactionWatch struct {
	peer, id string
	source   session.ChannelContext
	native   atomic.Pointer[engine.Submission]
}

// TrackReaction is called only for a new, committed input. No stored inputs or
// reaction requests are replayed on restart. Under load, drop new observations.
func (m *Manager) TrackReaction(peer, id string, source session.ChannelContext) {
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
	allow := m.reactionAllowed[platform]
	if allow == nil {
		return
	}
	last := channel.Reaction(0)
	previous := ""
	status := channel.ReactionQueued
	policy := m.policies[platform]
	if policy != nil {
		handled, err := policy.AcknowledgeQueue(ctx, bot, watch.peer, watch.id, watch.source, watch.nativeQueued)
		if errors.Is(err, channel.ErrReactionUnknown) {
			return
		}
		if handled {
			last = channel.ReactionQueued
			status = m.reactionState(ctx, watch, last)
		}
	}
	tick := time.NewTicker(reactionPoll)
	defer tick.Stop()
	for {
		if status > last {
			callCtx, cancel := context.WithTimeout(ctx, reactionTimeout)
			allowed := allow(callCtx, watch.peer, watch.source)
			var err error
			if allowed {
				previous, err = bot.React(callCtx, watch.peer, watch.source.MessageID, previous, status)
			}
			cancel()
			if !allowed || errors.Is(err, channel.ErrReactionUnknown) || status >= channel.ReactionSucceeded {
				return
			}
			last = status
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		status = m.reactionState(ctx, watch, last)
	}
}

func (watch *reactionWatch) nativeQueued(ctx context.Context) (queued, published bool) {
	if sub := watch.native.Load(); sub != nil {
		queued, err := sub.Queued(ctx)
		return err == nil && queued, true
	}
	return false, false
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
	case "sent":
		return channel.ReactionSucceeded
	case "failed":
		return channel.ReactionFailed
	case "superseded":
		return channel.ReactionCancelled
	default:
		return last
	}
}
