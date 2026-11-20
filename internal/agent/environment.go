package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/recally-io/dex/internal/db/sqlc"
	"github.com/recally-io/dex/internal/execenv"
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
// bot start in the workspace of the new environment. A session that selects the earlier
// environment explicitly keeps its binding, so the earlier environment goes on the allowlist.
func (s *Service) environmentChanged(ctx context.Context, old sqlc.Agent, environmentID string) error {
	if old.EnvironmentID == environmentID {
		return nil
	}
	if err := s.q.ClearDefaultSessionCwd(ctx, old.ID); err != nil {
		return err
	}
	earlier := old.EnvironmentID
	if earlier == "" {
		earlier = execenv.LocalID
	}
	n, err := s.q.CountEnvironmentSessions(ctx, sqlc.CountEnvironmentSessionsParams{AgentID: old.ID, EnvironmentID: earlier})
	if err != nil || n == 0 {
		return err
	}
	return s.q.AddAgentEnvironment(ctx, sqlc.AddAgentEnvironmentParams{AgentID: old.ID, EnvironmentID: earlier, CreatedAt: time.Now().UnixMilli()})
}
