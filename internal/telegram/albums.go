package telegram

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// WakeMedia wakes collection after a stored member, a scope reset or native delivery.
func (b *Bot) WakeMedia() {
	select {
	case b.albumWake <- struct{}{}:
	default:
	}
}

func (b *Bot) collectAlbums(ctx context.Context) {
	for {
		delay, err := b.flushAlbums(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("telegram: album collection waits for retry", "err", err)
			// Admission backoff must not delay collection of new members.
			delay = min(delay, 5*time.Second)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-b.albumWake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (b *Bot) flushAlbums(ctx context.Context) (time.Duration, error) {
	if err := b.chats.CleanupTelegramAlbums(ctx, b.cfg.Uploads); err != nil {
		return 5 * time.Second, err
	}
	batches, delay, err := b.chats.ReadyTelegramAlbums(ctx)
	if err != nil {
		return 5 * time.Second, err
	}
	for _, batch := range batches {
		err := b.chats.Receive(ctx, batch.Peer, batch.Source, batch.Message)
		receipt, lookupErr := b.chats.Source(ctx, batch.Source)
		if lookupErr != nil {
			return delay, errors.Join(err, lookupErr)
		}
		if receipt.ID == "" {
			return delay, errors.Join(err, errors.New("album input receipt is missing"))
		}
		// A committed input owns these uploads even when native admission needs reconciliation.
		if finishErr := b.chats.FinishTelegramAlbum(ctx, batch.Source); finishErr != nil {
			return delay, errors.Join(err, finishErr)
		}
		chatID, parseErr := peerID(batch.Peer)
		if parseErr != nil {
			return delay, parseErr
		}
		b.startPreview(ctx, batch.Source, chatID)
		if err != nil {
			return delay, err
		}
	}
	return delay, nil
}
