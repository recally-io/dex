package channel

import (
	"context"
	"errors"
	"time"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/upload"
)

func (m *Manager) StoreTelegramAlbum(ctx context.Context, peer, source string, msg chat.Message) error {
	return m.chats.StoreTelegramAlbum(ctx, m.agentID, peer, source, msg)
}

func (m *Manager) ReadyTelegramAlbums(ctx context.Context) ([]chat.TelegramAlbum, time.Duration, error) {
	return m.chats.ReadyTelegramAlbums(ctx, m.agentID)
}

func (m *Manager) FinishTelegramAlbum(ctx context.Context, source string) error {
	return m.chats.FinishTelegramAlbum(ctx, m.agentID, source)
}

// CleanupTelegramAlbums deletes only uploads that never transferred to a business input.
func (m *Manager) CleanupTelegramAlbums(ctx context.Context, store *upload.Store) error {
	pending, err := m.chats.DiscardedTelegramUploads(ctx, m.agentID)
	if err != nil {
		return err
	}
	for _, item := range pending {
		for _, file := range item.Attachments {
			if err := store.Remove(file.ID); err != nil && !errors.Is(err, upload.ErrNotFound) {
				return err
			}
		}
		if err := m.chats.ClearDiscardedTelegramUpload(ctx, m.agentID, item.Source); err != nil {
			return err
		}
	}
	return nil
}
