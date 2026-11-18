package chat

import (
	"context"

	"github.com/recally-io/dex/internal/execenv"
)

type DirectoryEntry = execenv.DirectoryEntry

type DirectoryListing = execenv.DirectoryListing

// ListDirectories exposes names and paths in the selected agent's execution environment.
func (s *Service) ListDirectories(ctx context.Context, agentID string, path *string) (DirectoryListing, error) {
	return s.engine.ListDirectories(ctx, agentID, path)
}
