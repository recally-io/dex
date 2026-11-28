package bot

import (
	"context"

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

// RestoreBotLocked clears the archive and runs beforeCommit in the same transaction.
// The caller holds the shared settings lock.
func (s *Service) RestoreBotLocked(ctx context.Context, id string, beforeCommit func(context.Context, *sqlc.Queries) error) error {
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
	if err := beforeCommit(ctx, q); err != nil {
		return err
	}
	return tx.Commit()
}
