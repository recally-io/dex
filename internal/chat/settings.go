package chat

import (
	"context"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// SetChat replaces the settings that chat chatID overrides. The next message of the chat uses them.
func (s *Service) SetChat(ctx context.Context, agentID string, chatID int64, c Settings, check func(context.Context, Settings) error) error {
	if err := check(ctx, c); err != nil {
		return err
	}
	return s.q.SetConversationSettings(ctx, sqlc.SetConversationSettingsParams{
		ModelProvider: c.Provider, ModelID: c.ModelID, ThinkingLevel: c.ThinkingLevel, ID: chatID, AgentID: agentID,
	})
}
