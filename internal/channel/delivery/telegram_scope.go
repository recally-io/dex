package delivery

import (
	"context"

	"github.com/recally-io/dex/internal/chat"
	"github.com/recally-io/dex/internal/upload"
)

func (m *Manager) TelegramInputAllowed(ctx context.Context, peer string, source chat.ChannelContext) (bool, error) {
	return m.chats.TelegramInputAllowed(ctx, m.agentID, peer, source)
}

func (m *Manager) GroupUploads(ctx context.Context, peer string, source chat.ChannelContext) (*upload.Store, error) {
	return m.chats.GroupUploads(ctx, m.agentID, peer, source)
}
