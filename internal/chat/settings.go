package chat

import (
	"context"
	"errors"
	"strings"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// ErrSettingsForbidden is a settings change for a session that takes no input and is not an automation.
var ErrSettingsForbidden = errors.New("this session is read-only; its model cannot change")

// SetChat replaces the settings that chat chatID overrides. The next message of the chat uses them.
func (s *Service) SetChat(ctx context.Context, agentID string, chatID int64, c Settings, check func(context.Context, Settings) error) error {
	// One rule with SetProject: a session that accepts input accepts settings. Automation sessions
	// take no input but keep their own model for the next run.
	chat, err := s.Get(ctx, agentID, chatID)
	if err != nil {
		return err
	}
	if !chat.Writable && !strings.HasPrefix(chat.ChannelKey, "automation:") {
		return ErrSettingsForbidden
	}
	if err := check(ctx, c); err != nil {
		return err
	}
	return s.q.SetConversationSettings(ctx, sqlc.SetConversationSettingsParams{
		ModelProvider: c.Provider, ModelID: c.ModelID, ThinkingLevel: c.ThinkingLevel, ID: chatID, AgentID: agentID,
	})
}
