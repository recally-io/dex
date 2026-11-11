package agent

import (
	"context"
	"errors"

	"github.com/recally-io/dex/internal/feishu"
	"github.com/recally-io/dex/internal/telegram"
	"github.com/recally-io/dex/internal/wechat"
)

// ListArchivedAgents returns the archived agents, newest archive first.
func (s *Service) ListArchivedAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.q.ListArchivedAgents(ctx)
	if err != nil {
		return nil, err
	}
	agents := make([]Agent, len(rows))
	for i, row := range rows {
		agents[i] = agentView(row)
	}
	return agents, nil
}

// StopChannels stops the channels of a bot that another path archived. Settings stay.
func (s *Service) StopChannels(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncChannelsLocked(ctx, id, false)
}

// RestoreAgent shows an archived bot again and starts the channels that are configured.
func (s *Service) RestoreAgent(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.q.RestoreAgent(ctx, id)
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return s.syncChannelsLocked(ctx, id, true)
}

// syncChannelsLocked starts the saved channels of a bot, or stops all of them. The caller holds
// s.mu, which orders it with every other channel change.
func (s *Service) syncChannelsLocked(ctx context.Context, id string, run bool) error {
	var (
		tg *telegram.Config
		fs *feishu.Config
		wx *wechat.Config
	)
	if run {
		var errTG, errFS, errWX error
		tg, errTG = s.TelegramConfig(ctx, id)
		fs, errFS = s.FeishuConfig(ctx, id)
		wx, errWX = s.WechatConfig(ctx, id)
		if err := errors.Join(errTG, errFS, errWX); err != nil {
			return err
		}
	}
	if s.onTelegram != nil {
		s.onTelegram(id, tg)
	}
	if s.onFeishu != nil {
		s.onFeishu(id, fs)
	}
	if s.onWechat != nil {
		s.onWechat(id, wx)
	}
	return nil
}
