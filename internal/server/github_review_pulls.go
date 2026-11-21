package server

import (
	"context"

	"github.com/recally-io/dex/internal/api"
)

func (h *handler) SaveGitHubReviewPull(ctx context.Context, req api.SaveGitHubReviewPullRequestObject) (api.SaveGitHubReviewPullResponseObject, error) {
	if req.Body == nil || req.Body.Token == nil {
		return nil, errInvalid
	}
	if err := h.Settings.SaveGitHubReviewPull(ctx, req.RepositoryId, req.PullNumber, *req.Body.Token); err != nil {
		return nil, err
	}
	return api.SaveGitHubReviewPull204Response{}, nil
}

func (h *handler) DeleteGitHubReviewPull(ctx context.Context, req api.DeleteGitHubReviewPullRequestObject) (api.DeleteGitHubReviewPullResponseObject, error) {
	if err := h.Settings.DeleteGitHubReviewPull(ctx, req.RepositoryId, req.PullNumber); err != nil {
		return nil, err
	}
	return api.DeleteGitHubReviewPull204Response{}, nil
}
