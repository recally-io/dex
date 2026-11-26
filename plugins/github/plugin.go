package github

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/plugin"
	tasksvc "github.com/recally-io/dex/internal/task"
	"github.com/recally-io/dex/plugins/github/api"
	"github.com/recally-io/dex/plugins/github/store"
)

var errInvalid = errors.New("invalid request")

// New makes the GitHub plugin: the signed Webhook, review Pull, issue Tasks, check suites, and PR links.
func New(deps plugin.Deps) plugin.Plugin {
	s := &service{q: store.New(deps.DB), tasks: deps.Tasks}
	hooks := http.NewServeMux()
	// Users paste this URL into GitHub, so it must not change.
	hooks.HandleFunc("POST /hooks/github/{repositoryId}", s.receive)
	routes := http.NewServeMux()
	api.HandlerWithOptions(api.NewStrictHandlerWithOptions(s, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadRequest, err.Error())
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, errInvalid), errors.Is(err, tasksvc.ErrInvalid):
				status = http.StatusBadRequest
			case errors.Is(err, sql.ErrNoRows):
				status = http.StatusNotFound
			case errors.Is(err, tasksvc.ErrObjectConflict):
				status = http.StatusConflict
			default:
				slog.Error("github plugin api", "err", err)
			}
			writeError(w, status, err.Error())
		},
	}), api.StdHTTPServerOptions{BaseRouter: routes})
	pull := NewPuller(s.q, s.tasks)
	return plugin.Plugin{
		Name: "github", Hooks: hooks, HookLimit: MaxPayload, API: routes, Migrations: Migrations,
		Jobs:  []plugin.Job{{Name: "review-pull", Every: time.Minute, Run: pull.Check}},
		Tools: []tasksvc.LinkTool{pullTool},
	}
}

type service struct {
	q     *store.Queries
	tasks *tasksvc.Service
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.MarshalWrite(w, api.Error{Error: message}); err != nil {
		slog.Error("write response", "err", err)
	}
}

func (s *service) receive(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("repositoryId"), 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != r.PathValue("repositoryId") {
		http.NotFound(w, r)
		return
	}
	config, err := s.q.GetGitHubWebhook(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("load GitHub webhook secret", "repository", id, "err", err)
		http.Error(w, "cannot accept delivery", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		status := http.StatusBadRequest
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "cannot read delivery", status)
		return
	}
	_, err = AcceptWebhook(r.Context(), s.tasks, id, []byte(config.Secret), r.Header.Get("X-GitHub-Delivery"), r.Header.Get("X-GitHub-Event"), r.Header.Get("X-Hub-Signature-256"), body, Rule{IssueBotID: config.IssueBotID.String, HoldUnmatched: config.HoldUnmatched == 1})
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrSignature):
		http.Error(w, "invalid signature", http.StatusUnauthorized)
	case errors.Is(err, ErrPayload), errors.Is(err, tasksvc.ErrInvalid):
		http.Error(w, "invalid delivery", http.StatusBadRequest)
	default:
		slog.Error("store GitHub webhook delivery", "repository", id, "err", err)
		http.Error(w, "cannot accept delivery", http.StatusServiceUnavailable)
	}
}

// SaveGitHubWebhook replaces the write-only secret for one repository. holdUnmatched keeps
// Webhook and Pull events that match no Task for the owner.
func (s *service) SaveGitHubWebhook(ctx context.Context, req api.SaveGitHubWebhookRequestObject) (api.SaveGitHubWebhookResponseObject, error) {
	if req.Body == nil || req.Body.Secret == nil || req.RepositoryId <= 0 || len(*req.Body.Secret) < 32 || len(*req.Body.Secret) > 256 {
		return nil, fmt.Errorf("%w: use a positive repository ID and a secret of 32 to 256 bytes", errInvalid)
	}
	issueBot := *cmp.Or(req.Body.IssueBotId, new(""))
	hold := int64(0)
	if req.Body.HoldUnmatched != nil && *req.Body.HoldUnmatched {
		hold = 1
	}
	n, err := s.q.SaveGitHubWebhook(ctx, store.SaveGitHubWebhookParams{RepositoryID: req.RepositoryId, Secret: *req.Body.Secret, IssueBotID: sql.NullString{String: issueBot, Valid: issueBot != ""}, HoldUnmatched: hold})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("%w: select an active bot for new GitHub issues", errInvalid)
	}
	return api.SaveGitHubWebhook204Response{}, nil
}

// DeleteGitHubWebhook removes the secret and disables new deliveries.
func (s *service) DeleteGitHubWebhook(ctx context.Context, req api.DeleteGitHubWebhookRequestObject) (api.DeleteGitHubWebhookResponseObject, error) {
	if req.RepositoryId <= 0 {
		return nil, errInvalid
	}
	if err := s.q.DeleteGitHubWebhook(ctx, req.RepositoryId); err != nil {
		return nil, err
	}
	return api.DeleteGitHubWebhook204Response{}, nil
}

// SaveGitHubReviewPull enables polling and clears the checkpoint of the previous configuration.
func (s *service) SaveGitHubReviewPull(ctx context.Context, req api.SaveGitHubReviewPullRequestObject) (api.SaveGitHubReviewPullResponseObject, error) {
	if req.Body == nil || req.Body.Token == nil {
		return nil, errInvalid
	}
	token := *req.Body.Token
	if req.RepositoryId <= 0 || req.PullNumber <= 0 || len(token) == 0 || len(token) > 512 || strings.IndexFunc(token, func(r rune) bool { return r < '!' || r > '~' }) >= 0 {
		return nil, fmt.Errorf("%w: use a positive repository ID, PR number, and GitHub token", errInvalid)
	}
	if err := s.q.SaveGitHubReviewPull(ctx, store.SaveGitHubReviewPullParams{RepositoryID: req.RepositoryId, PullNumber: req.PullNumber, Token: token, ConfigurationID: uuid.NewV7().String()}); err != nil {
		return nil, err
	}
	return api.SaveGitHubReviewPull204Response{}, nil
}

// DeleteGitHubReviewPull disables polling for this PR.
func (s *service) DeleteGitHubReviewPull(ctx context.Context, req api.DeleteGitHubReviewPullRequestObject) (api.DeleteGitHubReviewPullResponseObject, error) {
	if req.RepositoryId <= 0 || req.PullNumber <= 0 {
		return nil, errInvalid
	}
	if err := s.q.DeleteGitHubReviewPull(ctx, store.DeleteGitHubReviewPullParams{RepositoryID: req.RepositoryId, PullNumber: req.PullNumber}); err != nil {
		return nil, err
	}
	return api.DeleteGitHubReviewPull204Response{}, nil
}

// BindTaskGitHubPull links a PR to a Task and can add a wait for a review of one commit.
func (s *service) BindTaskGitHubPull(ctx context.Context, req api.BindTaskGitHubPullRequestObject) (api.BindTaskGitHubPullResponseObject, error) {
	if req.Body == nil || req.Body.RepositoryId <= 0 || req.Body.PullNumber <= 0 {
		return nil, errInvalid
	}
	namespace, object := Namespace(req.Body.RepositoryId), PullRequest(req.Body.PullNumber)
	var wait *tasksvc.Wait
	if condition := req.Body.Wait; condition != nil {
		if condition.Revision == "" || strings.TrimSpace(condition.Revision) != condition.Revision {
			return nil, errInvalid
		}
		wait = new(tasksvc.Wait{Namespace: namespace, Object: object, Type: ReviewSubmitted, AfterAt: condition.AfterAt, Revision: condition.Revision, Alert: condition.Alert != nil && *condition.Alert})
	}
	if err := s.tasks.BindObject(ctx, req.BotId, req.TaskId, namespace, object, wait); err != nil {
		return nil, err
	}
	return api.BindTaskGitHubPull204Response{}, nil
}

// pullTool replaces githubPull of task_update. The core writes the link with its replay record.
var pullTool = tasksvc.LinkTool{
	Name:        "task_link_github_pull",
	Description: "Link a GitHub PR to a Task. waitRevision also waits for a new review of that commit.",
	Rules:       "When a child reports a GitHub PR for a Task, link it with task_link_github_pull. Set alertMain when the next step needs the review.",
	Required:    []string{"repositoryId", "pullNumber"},
	Properties: map[string]any{
		"repositoryId": map[string]any{"type": "integer", "minimum": 1, "description": "The numeric GitHub repository ID, not the owner/name."},
		"pullNumber":   map[string]any{"type": "integer", "minimum": 1},
		"waitRevision": map[string]any{"type": "string", "minLength": 1, "description": "A commit SHA. A review of it by an owner, member, or collaborator resolves the wait."},
		"alertMain":    map[string]any{"type": "boolean", "description": "Send Main a Task alert when the review arrives. Use it when the next step needs the review. The default is false."},
	},
	Parse: func(raw []byte) (tasksvc.Link, error) {
		var a struct {
			RepositoryID int64  `json:"repositoryId"`
			PullNumber   int64  `json:"pullNumber"`
			WaitRevision string `json:"waitRevision"`
			AlertMain    bool   `json:"alertMain"`
		}
		if err := json.Unmarshal(raw, &a); err != nil || a.RepositoryID <= 0 || a.PullNumber <= 0 || strings.TrimSpace(a.WaitRevision) != a.WaitRevision || len(a.WaitRevision) > 512 {
			return tasksvc.Link{}, errors.New("give a numeric repositoryId, a pullNumber, and an optional commit SHA")
		}
		link := tasksvc.Link{Namespace: Namespace(a.RepositoryID), Object: PullRequest(a.PullNumber), Alert: a.AlertMain, Name: fmt.Sprintf("PR %d of repository %d", a.PullNumber, a.RepositoryID)}
		if a.WaitRevision != "" {
			link.WaitEvent, link.WaitRevision = ReviewSubmitted, a.WaitRevision
		}
		return link, nil
	},
}
