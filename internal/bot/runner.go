package bot

import (
	"context"
	"errors"
	"fmt"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// saveSettings stores the settings of the bot old. A change of its environment runs in one
// write transaction with the work check and the cwd reset: input and background commands that
// commit first block the change, and input that commits later runs in the new environment. The
// sessions that select an environment explicitly keep it, because a session selects only local
// or SSH, which keep no per-bot state.
func (s *Service) saveSettings(ctx context.Context, old sqlc.Bot, p sqlc.UpdateBotSettingsParams) (err error) {
	if old.RunnerID == p.RunnerID {
		return s.q.UpdateBotSettings(ctx, p)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	// Take the write lock first, so no input commits between the count and the update.
	if _, err := tx.ExecContext(ctx, "UPDATE bots SET id = id WHERE 0"); err != nil {
		return err
	}
	q := s.q.WithTx(tx)
	n, err := q.CountDefaultSessionWork(ctx, old.ID)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: wait for %d items to end, or stop them", ErrRunnerBusy, n)
	}
	if err := q.UpdateBotSettings(ctx, p); err != nil {
		return err
	}
	if err := q.ClearDefaultSessionCwd(ctx, old.ID); err != nil {
		return err
	}
	if err := q.ClearDefaultAutomationCwd(ctx, old.ID); err != nil {
		return err
	}
	return tx.Commit()
}
