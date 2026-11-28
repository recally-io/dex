package bot

import (
	"context"
	"errors"

	"github.com/recally-io/dex/internal/db"
	"github.com/recally-io/dex/internal/db/sqlc"
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
	if err == nil {
		return nil // Restore can finish before the archive callback reaches this lock.
	}
	if errors.Is(err, db.ErrBotArchived) {
		err = nil
	}
	// Also stop when the read fails, so an archived bot does not keep its channels.
	if s.onTelegram != nil {
		s.onTelegram(id, nil)
	}
	if s.feishu != nil {
		s.feishu.StopLocked(id)
	}
	if s.wechat != nil {
		s.wechat.StopLocked(id)
	}
	return err
}

// RestoreBot shows an archived bot again and starts the channels that are configured.
func (s *Service) RestoreBot(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.restoreBotLocked(ctx, id); err != nil {
		return err
	}
	// Do not let a pre-archive setup become valid again if stop has not run yet.
	if s.feishu != nil {
		s.feishu.CancelRegistrationLocked(id)
	}
	if s.wechat != nil {
		s.wechat.CancelLoginLocked(id)
	}
	tg, errTG := s.TelegramConfig(ctx, id)
	var startFeishu func(context.Context)
	var errFS error
	if s.feishu != nil {
		startFeishu, errFS = s.feishu.PrepareStartLocked(ctx, id)
	}
	var startWechat func(context.Context)
	var errWX error
	if s.wechat != nil {
		startWechat, errWX = s.wechat.PrepareStartLocked(ctx, id)
	}
	if err := errors.Join(errTG, errFS, errWX); err != nil {
		return err
	}
	if s.onTelegram != nil && (tg == nil || db.CanStartChannel(ctx, s.q, id)) {
		s.onTelegram(id, tg)
	}
	if startFeishu != nil {
		startFeishu(ctx)
	}
	if startWechat != nil {
		startWechat(ctx)
	}
	return nil
}

func (s *Service) restoreBotLocked(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := sqlc.New(tx)
	rows, err := q.RestoreBot(ctx, id)
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	// Restore and cancellation must commit together. The manager can replay a
	// pre-archive setup even if this process never received the stop callback.
	if err := q.CancelBotTelegramManagedSetups(ctx, id); err != nil {
		return err
	}
	return tx.Commit()
}
