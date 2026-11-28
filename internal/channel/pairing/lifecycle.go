package pairing

import (
	"context"
	"errors"

	"github.com/recally-io/dex/internal/db"
)

// StopChannels stops the runtimes after an archive commits. Settings stay.
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
	s.telegram.StopLocked(id)
	s.feishu.StopLocked(id)
	s.wechat.StopLocked(id)
	return err
}

// RestoreChannelsLocked starts the saved channels after the restore transaction
// commits. The caller holds the settings lock across the transaction and this call.
func (s *Service) RestoreChannelsLocked(ctx context.Context, id string) error {
	// Do not let a pre-archive setup become valid again if stop has not run yet.
	s.feishu.CancelRegistrationLocked(id)
	s.wechat.CancelLoginLocked(id)
	startTelegram, errTG := s.telegram.PrepareStartLocked(ctx, id)
	startFeishu, errFS := s.feishu.PrepareStartLocked(ctx, id)
	startWechat, errWX := s.wechat.PrepareStartLocked(ctx, id)
	if err := errors.Join(errTG, errFS, errWX); err != nil {
		return err
	}
	startTelegram(ctx)
	startFeishu(ctx)
	startWechat(ctx)
	return nil
}
