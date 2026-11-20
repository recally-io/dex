package server

import (
	"context"
	"errors"
	"strings"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/worktask"
	"github.com/recally-io/dex/internal/worktask/github"
)

func taskGitHubPulls(detail worktask.Detail) []api.WorkTaskGitHubPull {
	type key struct{ namespace, object string }
	waits := make(map[key][]api.WorkTaskReviewWait)
	for _, wait := range detail.Waits {
		if wait.ObjectType == "pull_request" && wait.EventType == github.ReviewSubmitted {
			k := key{wait.Namespace, wait.ObjectKey}
			waits[k] = append(waits[k], api.WorkTaskReviewWait{Id: wait.ID, Revision: wait.Revision, AfterAt: wait.AfterAt, Resolved: wait.ResolvedBy.Valid})
		}
	}
	pulls := []api.WorkTaskGitHubPull{}
	for _, object := range detail.Objects {
		repo, pull, ok := github.PullCoordinates(object.Namespace, worktask.Object{Type: object.ObjectType, Key: object.ObjectKey})
		if !ok {
			continue
		}
		reviewWaits := waits[key{object.Namespace, object.ObjectKey}]
		if reviewWaits == nil {
			reviewWaits = []api.WorkTaskReviewWait{}
		}
		pulls = append(pulls, api.WorkTaskGitHubPull{RepositoryId: repo, PullNumber: pull, Waits: reviewWaits})
	}
	return pulls
}

func (h *handler) BindTaskGitHubPull(ctx context.Context, req api.BindTaskGitHubPullRequestObject) (api.BindTaskGitHubPullResponseObject, error) {
	if req.Body == nil || req.Body.RepositoryId <= 0 || req.Body.PullNumber <= 0 {
		return nil, errInvalid
	}
	namespace := github.Namespace(req.Body.RepositoryId)
	object := github.PullRequest(req.Body.PullNumber)
	var wait *worktask.Wait
	if condition := req.Body.Wait; condition != nil {
		if condition.Revision == "" || strings.TrimSpace(condition.Revision) != condition.Revision {
			return nil, errInvalid
		}
		wait = new(worktask.Wait{Namespace: namespace, Object: object, Type: github.ReviewSubmitted, AfterAt: condition.AfterAt, Revision: condition.Revision})
	}
	if err := h.WorkTasks.BindObject(ctx, req.AgentId, req.TaskId, namespace, object, wait); err != nil {
		if errors.Is(err, worktask.ErrObjectConflict) {
			return api.BindTaskGitHubPulldefaultJSONResponse{StatusCode: 409, Body: api.Error{Error: err.Error()}}, nil
		}
		return nil, workTaskError(err)
	}
	return api.BindTaskGitHubPull204Response{}, nil
}
