package settings

import (
	"context"
	"fmt"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// SaveGitHubWebhook replaces the write-only secret for one repository.
func (s *Service) SaveGitHubWebhook(ctx context.Context, repositoryID int64, secret string) error {
	if repositoryID <= 0 || len(secret) < 32 || len(secret) > 256 {
		return fmt.Errorf("%w: use a positive repository ID and a secret of 32 to 256 bytes", ErrInvalid)
	}
	return s.q.SaveGitHubWebhook(ctx, sqlc.SaveGitHubWebhookParams{RepositoryID: repositoryID, Secret: secret})
}

// GitHubWebhookSecret reads a secret for verification only.
func (s *Service) GitHubWebhookSecret(ctx context.Context, repositoryID int64) (string, error) {
	return s.q.GetGitHubWebhookSecret(ctx, repositoryID)
}

// DeleteGitHubWebhook removes the secret and disables new deliveries.
func (s *Service) DeleteGitHubWebhook(ctx context.Context, repositoryID int64) error {
	if repositoryID <= 0 {
		return ErrInvalid
	}
	return s.q.DeleteGitHubWebhook(ctx, repositoryID)
}
