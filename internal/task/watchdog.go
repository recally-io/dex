package task

import (
	"context"
	"log/slog"
	"time"
)

func (s *Service) ExtendActiveLeasesOnStartup(ctx context.Context, now time.Time, grace time.Duration) error {
	return s.q.ExtendActiveExternalLeases(ctx, externalTime(now.Add(grace).UnixMilli()))
}

func (s *Service) ExpireExternalLeases(ctx context.Context, at time.Time) error {
	return s.write(ctx, func(l *Ledger, _ int64) error {
		now := at.UnixMilli()
		expired, err := l.q.ExpireExternalExecutions(ctx, externalTime(now))
		if err != nil {
			return err
		}
		for _, e := range expired {
			task, err := Lookup(ctx, l.q, e.TaskID)
			if err != nil {
				return err
			}
			if err := l.recordExternalExecution(ctx, System, task, e, "", now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) RunLeaseWatchdog(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := s.ExpireExternalLeases(ctx, time.Now()); err != nil && ctx.Err() == nil {
				slog.Error("expire external leases", "err", err)
			}
		}
	}
}
