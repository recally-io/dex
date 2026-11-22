package server

import (
	"context"
	"fmt"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/feedback"
)

func (h *handler) BuildFeedbackDraft(_ context.Context, req api.BuildFeedbackDraftRequestObject) (api.BuildFeedbackDraftResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	draft, err := feedback.Build(req.Body.Title, req.Body.Description)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errInvalid, err)
	}
	return api.BuildFeedbackDraft200JSONResponse{Title: draft.Title, Description: draft.Description, Repository: feedback.Repository, NewIssueUrl: feedback.IssueURL}, nil
}
