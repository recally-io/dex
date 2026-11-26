package delivery

import (
	"context"
	"errors"
	"time"

	"github.com/recally-io/dex/internal/session"
	"github.com/recally-io/dex/internal/upload"
)

func (m *Manager) KnownTelegramAlbum(ctx context.Context, peer string, identity session.ChannelContext) (bool, error) {
	return m.sessions.KnownTelegramAlbum(ctx, m.botID, peer, identity)
}

func (m *Manager) StoreTelegramAlbum(ctx context.Context, peer, source string, msg session.Message) error {
	return m.sessions.StoreTelegramAlbum(ctx, m.botID, peer, source, msg)
}

func (m *Manager) ReadyTelegramAlbums(ctx context.Context) ([]session.TelegramAlbum, time.Duration, error) {
	return m.sessions.ReadyTelegramAlbums(ctx, m.botID)
}

func (m *Manager) FinishTelegramAlbum(ctx context.Context, source string) error {
	return m.sessions.FinishTelegramAlbum(ctx, m.botID, source)
}

// CleanupTelegramAlbums deletes only uploads that never transferred to a business input.
func (m *Manager) CleanupTelegramAlbums(ctx context.Context, store *upload.Store) error {
	pending, err := m.sessions.DiscardedTelegramUploads(ctx, m.botID)
	if err != nil {
		return err
	}
	for _, item := range pending {
		for _, file := range item.Attachments {
			if err := store.Remove(file.ID); err != nil && !errors.Is(err, upload.ErrNotFound) {
				return err
			}
		}
		if err := m.sessions.ClearDiscardedTelegramUpload(ctx, m.botID, item.Source); err != nil {
			return err
		}
	}
	return m.cleanupSpeechInputs(ctx, store)
}
