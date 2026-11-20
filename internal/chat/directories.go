package chat

import (
	"context"

	"github.com/recally-io/dex/internal/execenv"
)

type DirectoryEntry = execenv.DirectoryEntry

type DirectoryListing = execenv.DirectoryListing

// ListDirectories exposes names and paths in an execution environment of the agent. An empty
// environmentID selects the environment of the agent.
func (s *Service) ListDirectories(ctx context.Context, agentID, environmentID string, path *string) (DirectoryListing, error) {
	return s.engine.ListDirectories(ctx, agentID, environmentID, path)
}
