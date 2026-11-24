package delivery

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/engine"
)

const (
	maxReactionWatches = 64
	reactionTimeout    = 5 * time.Second
	reactionPoll       = 150 * time.Millisecond
	queueAckWindow     = 30 * time.Second
	queueAckAttempts   = 3
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
	if platform == "telegram" && !strings.HasPrefix(watch.peer, "telegram:-") {
		if err := m.acknowledgeTelegramQueue(ctx, bot, watch); errors.Is(err, channel.ErrReactionUnknown) {
			return
		}
		last = channel.ReactionQueued
		status = m.reactionState(ctx, watch, last)
	}
	tick := time.NewTicker(reactionPoll)
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

// The same watcher owns the acknowledgement and later reactions. An unknown
// mutation ends the watcher because Telegram may still apply it.
func (m *Manager) acknowledgeTelegramQueue(ctx context.Context, bot channel.Reactor, watch *reactionWatch) error {
	deadline := time.Now().Add(queueAckWindow)
	for attempt := range queueAckAttempts {
		callCtx, cancel := context.WithTimeout(ctx, reactionTimeout)
		allowed := m.reactionAllowed(callCtx, "telegram", watch)
		if allowed && attempt == 0 {
			// Claim before the remote call. A crash leaves an unknown outcome, not
			// permission to send again.
			claimed, err := m.q.ClaimTelegramQueueAcknowledgement(callCtx, sqlc.ClaimTelegramQueueAcknowledgementParams{
				SubmissionID: watch.id, UpdatedAt: time.Now().UnixMilli(),
			})
			if err != nil && ctx.Err() == nil {
				slog.Warn("telegram: claim queued acknowledgement", "submission", watch.id, "err", err)
			}
			allowed = err == nil && claimed == 1
		}
		if allowed && attempt > 0 {
			_, queued := m.telegramQueueState(callCtx, watch)
			allowed = queued && time.Now().Before(deadline)
		}
		if !allowed {
			cancel()
			return nil
		}
		if err := callCtx.Err(); err != nil {
			cancel()
			return err
		}
		_, err := bot.React(callCtx, watch.peer, watch.source.MessageID, "", channel.ReactionQueued)
		retryAt := time.Now()
		cancel()
		outcome := "accepted"
		if errors.Is(err, channel.ErrReactionUnknown) {
			outcome = "unknown"
		} else if err != nil {
			outcome = "failed"
		}
		// A bot can stop after Telegram has confirmed the request. Save the result
		// before the runtime joins this worker.
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reactionTimeout)
		saveErr := m.q.RecordTelegramQueueAcknowledgement(saveCtx, sqlc.RecordTelegramQueueAcknowledgementParams{
			SubmissionID: watch.id, Outcome: outcome, UpdatedAt: time.Now().UnixMilli(),
		})
		cancel()
		if saveErr != nil {
			slog.Warn("telegram: record queued acknowledgement", "submission", watch.id, "err", saveErr)
			return err
		}
		retry, ok := errors.AsType[*channel.RetryError](err)
		if !ok || attempt+1 == queueAckAttempts {
			return err
		}
		retryAt = retryAt.Add(max(time.Second<<attempt, retry.After))
		if !retryAt.Before(deadline) {
			return err
		}
		if !m.waitTelegramQueueRetry(ctx, watch, retryAt, deadline) {
			return err
		}
	}
	return nil
}

func (m *Manager) waitTelegramQueueRetry(ctx context.Context, watch *reactionWatch, retryAt, deadline time.Time) bool {
	for time.Now().Before(deadline) {
		pending, queued := m.telegramQueueState(ctx, watch)
		if !pending {
			return false
		}
		if queued && !time.Now().Before(retryAt) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(reactionPoll):
		}
	}
	return false
}

// A missing native handle is not proof that the input has not started. Wait for
// admission to publish the handle before retrying.
func (m *Manager) telegramQueueState(ctx context.Context, watch *reactionWatch) (pending, queued bool) {
	r, err := m.q.GetChannelReactionReceipt(ctx, watch.id)
	if err != nil || r.Status != "pending" || r.StopRequested != 0 {
		return false, false
	}
	if sub := watch.native.Load(); sub != nil {
		queued, err := sub.Queued(ctx)
		return err == nil && queued, err == nil && queued
	}
	return true, false
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
	enabled, err := m.q.TelegramPrivateReactionsEnabled(ctx, m.agentID)
	if err != nil || !enabled {
		return false
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
