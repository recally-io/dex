package session

import (
	"context"

	"github.com/recally-io/dex/internal/runner"
)

type DirectoryEntry = runner.DirectoryEntry

type DirectoryListing = runner.DirectoryListing

// ListDirectories exposes names and paths in an execution environment of the bot. An empty
// runnerID selects the environment of the bot.
func (s *Service) ListDirectories(ctx context.Context, botID, runnerID string, path *string) (DirectoryListing, error) {
	return s.engine.ListDirectories(ctx, botID, runnerID, path)
}
