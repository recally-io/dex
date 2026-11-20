package agent

import (
	"context"
	"fmt"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// checkEnvironmentChange refuses a change of the bot's environment while sessions that follow it
// have queued input or running background commands: they must not continue in another
// environment. Input that arrives between this check and the save runs in the new environment;
// a transaction around both is the fix if that window matters.
func (s *Service) checkEnvironmentChange(ctx context.Context, old sqlc.Agent, environmentID string) error {
	if old.EnvironmentID == environmentID {
		return nil
	}
	n, err := s.q.CountDefaultSessionWork(ctx, old.ID)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: wait for %d items to end, or stop them", ErrEnvironmentBusy, n)
	}
	return nil
}

// environmentChanged runs after a change of the bot's environment. The sessions that follow the
// bot start in the workspace of the new environment. A session that selects an environment
// explicitly keeps it: a session selects only local or SSH, which keep no per-bot state.
func (s *Service) environmentChanged(ctx context.Context, old sqlc.Agent, environmentID string) error {
	if old.EnvironmentID == environmentID {
		return nil
	}
	return s.q.ClearDefaultSessionCwd(ctx, old.ID)
}
