package worktask

import (
	"context"

	"github.com/recally-io/dex/internal/db/sqlc"
)

func (s *Service) BoardEventHead(ctx context.Context) (int64, error) {
	return s.q.BoardEventHead(ctx)
}

func (s *Service) BoardEventsAfter(ctx context.Context, cursor int64) ([]sqlc.ListBoardEventsRow, error) {
	return s.q.ListBoardEvents(ctx, cursor)
}
