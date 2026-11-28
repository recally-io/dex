package bot

import (
	"context"
	"errors"

	"github.com/recally-io/dex/internal/channel/feishu"
	"github.com/recally-io/dex/internal/channel/telegram"
	"github.com/recally-io/dex/internal/channel/wechat"
)

// ListArchivedBots returns the archived bots, newest archive first.
func (s *Service) ListArchivedBots(ctx context.Context) ([]Bot, error) {
	rows, err := s.q.ListArchivedBots(ctx)
	if err != nil {
		return nil, err
	}
	bots := make([]Bot, len(rows))
	for i, row := range rows {
		bots[i] = botView(row)
	}
	return bots, nil
}

// StopChannels stops the channels of a bot that another path archived, and drops its unfinished
// WeChat login and Feishu registration, so a poll that is already running cannot save credentials
// or start a channel after the archive. Settings stay.
func (s *Service) StopChannels(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.channelSession(id) = channelSession{}
	return s.syncChannelsLocked(ctx, id, false)
}

// RestoreBot shows an archived bot again and starts the channels that are configured.
func (s *Service) RestoreBot(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.q.RestoreBot(ctx, id)
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
	if s.onTelegram != nil && (tg == nil || s.canStartChannel(ctx, id)) {
		s.onTelegram(id, tg)
	}
	if s.onFeishu != nil && (fs == nil || s.canStartChannel(ctx, id)) {
		s.onFeishu(id, fs)
	}
	if s.onWechat != nil && (wx == nil || s.canStartChannel(ctx, id)) {
		s.onWechat(id, wx)
	}
	return nil
}
