package server

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/worktask"
	"github.com/recally-io/dex/internal/worktask/github"
)

func (h *handler) SaveGitHubWebhook(ctx context.Context, req api.SaveGitHubWebhookRequestObject) (api.SaveGitHubWebhookResponseObject, error) {
	if req.Body == nil || req.Body.Secret == nil {
		return nil, errInvalid
	}
	if err := h.Settings.SaveGitHubWebhook(ctx, req.RepositoryId, *req.Body.Secret, *cmp.Or(req.Body.IssueAgentId, new(""))); err != nil {
		return nil, err
	}
	return api.SaveGitHubWebhook204Response{}, nil
}

func (h *handler) DeleteGitHubWebhook(ctx context.Context, req api.DeleteGitHubWebhookRequestObject) (api.DeleteGitHubWebhookResponseObject, error) {
	if err := h.Settings.DeleteGitHubWebhook(ctx, req.RepositoryId); err != nil {
		return nil, err
	}
	return api.DeleteGitHubWebhook204Response{}, nil
}

func (h *handler) receiveGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("repositoryId"), 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != r.PathValue("repositoryId") {
		http.NotFound(w, r)
		return
	}
	config, err := h.Settings.GitHubWebhook(r.Context(), id)
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
	_, err = github.AcceptWebhook(r.Context(), h.WorkTasks, id, []byte(config.Secret), r.Header.Get("X-GitHub-Delivery"), r.Header.Get("X-GitHub-Event"), r.Header.Get("X-Hub-Signature-256"), body, config.IssueAgentID.String)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, github.ErrSignature):
		http.Error(w, "invalid signature", http.StatusUnauthorized)
	case errors.Is(err, github.ErrPayload), errors.Is(err, worktask.ErrInvalid):
		http.Error(w, "invalid delivery", http.StatusBadRequest)
	default:
		slog.Error("store GitHub webhook delivery", "repository", id, "err", err)
		http.Error(w, "cannot accept delivery", http.StatusServiceUnavailable)
	}
}
