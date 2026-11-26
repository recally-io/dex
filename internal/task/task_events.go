package task

import (
	"context"

	"github.com/recally-io/dex/internal/db/sqlc"
)

func (s *Service) TaskEventHead(ctx context.Context) (int64, error) {
	return s.q.TaskEventHead(ctx)
}

func (s *Service) TaskEventsAfter(ctx context.Context, cursor int64) ([]sqlc.ListTaskEventsRow, error) {
	return s.q.ListTaskEvents(ctx, cursor)
}
