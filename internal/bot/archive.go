package bot

import (
	"context"
	"errors"

	"github.com/recally-io/dex/internal/db"
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

// StopChannels stops channels and cancels setup after an archive commits. Settings stay.
func (s *Service) StopChannels(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := db.RequireActive(ctx, s.q, id)
	if !errors.Is(err, db.ErrBotArchived) {
		return err // Restore can finish before the archive callback reaches this lock.
	}
	delete(s.sessions, id)
	if s.onTelegram != nil {
		s.onTelegram(id, nil)
	}
	if s.feishu != nil {
		s.feishu.StopLocked(id)
	}
	if s.onWechat != nil {
		s.onWechat(id, nil)
	}
	return nil
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
	// Do not let a pre-archive setup become valid again if stop has not run yet.
	delete(s.sessions, id)
	if s.feishu != nil {
		s.feishu.CancelRegistrationLocked(id)
	}
	tg, errTG := s.TelegramConfig(ctx, id)
	var startFeishu func(context.Context)
	var errFS error
	if s.feishu != nil {
		startFeishu, errFS = s.feishu.PrepareStartLocked(ctx, id)
	}
	wx, errWX := s.WechatConfig(ctx, id)
	if err := errors.Join(errTG, errFS, errWX); err != nil {
		return err
	}
	if s.onTelegram != nil && (tg == nil || db.CanStartChannel(ctx, s.q, id)) {
		s.onTelegram(id, tg)
	}
	if startFeishu != nil {
		startFeishu(ctx)
	}
	if s.onWechat != nil && (wx == nil || db.CanStartChannel(ctx, s.q, id)) {
		s.onWechat(id, wx)
	}
	return nil
}
