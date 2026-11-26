package chat

import (
	"context"

	"github.com/recally-io/dex/internal/runner"
)

type DirectoryEntry = runner.DirectoryEntry

type DirectoryListing = runner.DirectoryListing

// ListDirectories exposes names and paths in an execution environment of the agent. An empty
// runnerID selects the environment of the agent.
func (s *Service) ListDirectories(ctx context.Context, agentID, runnerID string, path *string) (DirectoryListing, error) {
	return s.engine.ListDirectories(ctx, agentID, runnerID, path)
}
