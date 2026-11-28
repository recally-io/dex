package policy

import (
	"context"
	"errors"
	"time"

	"github.com/recally-io/dex/internal/session"
	"github.com/recally-io/dex/internal/upload"
)

func (p *Policy) StoreTelegramAlbum(ctx context.Context, peer, source string, msg session.Message) error {
	return p.sessions.StoreTelegramAlbum(ctx, p.botID, peer, source, msg)
}

func (p *Policy) ReadyTelegramAlbums(ctx context.Context) ([]session.TelegramAlbum, time.Duration, error) {
	return p.sessions.ReadyTelegramAlbums(ctx, p.botID)
}

func (p *Policy) FinishTelegramAlbum(ctx context.Context, source string) error {
	return p.sessions.FinishTelegramAlbum(ctx, p.botID, source)
}

// CleanupTelegramAlbums deletes only uploads that never transferred to a business input.
func (p *Policy) CleanupTelegramAlbums(ctx context.Context, store *upload.Store) error {
	pending, err := p.sessions.DiscardedTelegramUploads(ctx, p.botID)
	if err != nil {
		return err
	}
	for _, item := range pending {
		for _, file := range item.Attachments {
			if err := store.Remove(file.ID); err != nil && !errors.Is(err, upload.ErrNotFound) {
				return err
			}
		}
		if err := p.sessions.ClearDiscardedTelegramUpload(ctx, p.botID, item.Source); err != nil {
			return err
		}
	}
	return p.cleanupSpeechInputs(ctx, store)
}
