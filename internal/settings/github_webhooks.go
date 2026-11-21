package settings

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// SaveGitHubWebhook replaces the write-only secret for one repository.
func (s *Service) SaveGitHubWebhook(ctx context.Context, repositoryID int64, secret, issueAgentID string) error {
	if repositoryID <= 0 || len(secret) < 32 || len(secret) > 256 {
		return fmt.Errorf("%w: use a positive repository ID and a secret of 32 to 256 bytes", ErrInvalid)
	}
	n, err := s.q.SaveGitHubWebhook(ctx, sqlc.SaveGitHubWebhookParams{RepositoryID: repositoryID, Secret: secret, IssueAgentID: sql.NullString{String: issueAgentID, Valid: issueAgentID != ""}})
	if err == nil && n == 0 {
		return fmt.Errorf("%w: select an active agent for new GitHub issues", ErrInvalid)
	}
	return err
}

// GitHubWebhook reads webhook configuration for delivery verification only.
func (s *Service) GitHubWebhook(ctx context.Context, repositoryID int64) (sqlc.GithubWebhookSetting, error) {
	return s.q.GetGitHubWebhook(ctx, repositoryID)
}

// DeleteGitHubWebhook removes the secret and disables new deliveries.
func (s *Service) DeleteGitHubWebhook(ctx context.Context, repositoryID int64) error {
	if repositoryID <= 0 {
		return ErrInvalid
	}
	return s.q.DeleteGitHubWebhook(ctx, repositoryID)
}
