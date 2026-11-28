package policy

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/channel"
	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/session"
)

const (
	reactionTimeout  = 5 * time.Second
	reactionPoll     = 150 * time.Millisecond
	queueAckWindow   = 30 * time.Second
	queueAckAttempts = 3
)

func (p *Policy) AcknowledgeQueue(ctx context.Context, bot channel.Reactor, peer, id string, source session.ChannelContext, nativeQueued func(context.Context) (bool, bool)) (bool, error) {
	if strings.HasPrefix(peer, peerPrefix+"-") {
		return false, nil
	}
	return true, p.acknowledgeQueue(ctx, bot, peer, id, source, nativeQueued)
}

func (p *Policy) ReactionAllowed(ctx context.Context, peer string, source session.ChannelContext) bool {
	enabled, err := p.q.TelegramPrivateReactionsEnabled(ctx, p.botID)
	if err != nil || !enabled {
		return false
	}
	allowed, err := p.TelegramInputAllowed(ctx, peer, source)
	return err == nil && allowed
}

// The same watcher owns the acknowledgement and later reactions. An unknown
// mutation ends the watcher because Telegram may still apply it.
func (p *Policy) acknowledgeQueue(ctx context.Context, bot channel.Reactor, peer, id string, source session.ChannelContext, nativeQueued func(context.Context) (bool, bool)) error {
	deadline := time.Now().Add(queueAckWindow)
	for attempt := range queueAckAttempts {
		callCtx, cancel := context.WithTimeout(ctx, reactionTimeout)
		allowed := p.ReactionAllowed(callCtx, peer, source)
		if allowed {
			allowed = p.beginTelegramQueueAcknowledgement(callCtx, id, attempt > 0)
		}
		if allowed && attempt > 0 {
			// The durable write can wait for SQLite. Check again before sending.
			allowed = p.ReactionAllowed(callCtx, peer, source)
			_, queued := p.queueState(callCtx, id, nativeQueued)
			allowed = allowed && queued && time.Now().Before(deadline)
		}
		if !allowed {
			cancel()
			return nil
		}
		if err := callCtx.Err(); err != nil {
			cancel()
			return err
		}
		_, err := bot.React(callCtx, peer, source.MessageID, "", channel.ReactionQueued)
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
		saveErr := p.q.RecordTelegramQueueAcknowledgement(saveCtx, sqlc.RecordTelegramQueueAcknowledgementParams{
			SubmissionID: id, Outcome: outcome, UpdatedAt: time.Now().UnixMilli(),
		})
		cancel()
		if saveErr != nil {
			slog.Warn("telegram: record queued acknowledgement", "submission", id, "err", saveErr)
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
		if !p.waitQueueRetry(ctx, id, nativeQueued, retryAt, deadline) {
			return err
		}
	}
	return nil
}

// Record the unknown outcome before each request. A crash after this write
// cannot leave a prior rejection as the outcome of the current attempt.
func (p *Policy) beginTelegramQueueAcknowledgement(ctx context.Context, id string, retry bool) bool {
	var changed int64
	var err error
	if retry {
		changed, err = p.q.BeginTelegramQueueAcknowledgementRetry(ctx, sqlc.BeginTelegramQueueAcknowledgementRetryParams{
			SubmissionID: id, UpdatedAt: time.Now().UnixMilli(),
		})
	} else {
		changed, err = p.q.ClaimTelegramQueueAcknowledgement(ctx, sqlc.ClaimTelegramQueueAcknowledgementParams{
			SubmissionID: id, UpdatedAt: time.Now().UnixMilli(),
		})
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("telegram: begin queued acknowledgement", "submission", id, "err", err)
	}
	return err == nil && changed == 1
}

func (p *Policy) waitQueueRetry(ctx context.Context, id string, nativeQueued func(context.Context) (bool, bool), retryAt, deadline time.Time) bool {
	for time.Now().Before(deadline) {
		pending, queued := p.queueState(ctx, id, nativeQueued)
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
func (p *Policy) queueState(ctx context.Context, id string, nativeQueued func(context.Context) (bool, bool)) (pending, queued bool) {
	r, err := p.q.GetChannelReactionReceipt(ctx, id)
	if err != nil || r.Status != "pending" || r.StopRequested != 0 {
		return false, false
	}
	queued, published := nativeQueued(ctx)
	return !published || queued, queued
}
