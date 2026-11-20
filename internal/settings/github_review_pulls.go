package settings

import (
	"context"
	"fmt"
	"strings"
	"uuid"

	"github.com/recally-io/dex/internal/db/sqlc"
)

// SaveGitHubReviewPull enables polling and clears the previous configuration's checkpoint.
func (s *Service) SaveGitHubReviewPull(ctx context.Context, repository, pull int64, token string) error {
	if repository <= 0 || pull <= 0 || len(token) == 0 || len(token) > 512 || strings.IndexFunc(token, func(r rune) bool { return r < '!' || r > '~' }) >= 0 {
		return fmt.Errorf("%w: use a positive repository ID, PR number, and GitHub token", ErrInvalid)
	}
	return s.q.SaveGitHubReviewPull(ctx, sqlc.SaveGitHubReviewPullParams{RepositoryID: repository, PullNumber: pull, Token: token, ConfigurationID: uuid.NewV7().String()})
}

// DeleteGitHubReviewPull disables polling for this PR.
func (s *Service) DeleteGitHubReviewPull(ctx context.Context, repository, pull int64) error {
	if repository <= 0 || pull <= 0 {
		return ErrInvalid
	}
	return s.q.DeleteGitHubReviewPull(ctx, sqlc.DeleteGitHubReviewPullParams{RepositoryID: repository, PullNumber: pull})
}
