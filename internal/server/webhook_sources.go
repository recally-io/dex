package server

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/settings"
	"github.com/recally-io/dex/internal/worktask"
	"github.com/recally-io/dex/internal/worktask/webhook"
)

func (h *handler) SaveWebhookSource(ctx context.Context, req api.SaveWebhookSourceRequestObject) (api.SaveWebhookSourceResponseObject, error) {
	if req.Body == nil || req.Body.Secret == nil {
		return nil, errInvalid
	}
	if err := h.Settings.SaveWebhookSource(ctx, req.SourceId, *req.Body.Secret, *cmp.Or(req.Body.TaskAgentId, new("")), req.Body.HoldUnmatched != nil && *req.Body.HoldUnmatched); err != nil {
		return nil, err
	}
	return api.SaveWebhookSource204Response{}, nil
}

func (h *handler) DeleteWebhookSource(ctx context.Context, req api.DeleteWebhookSourceRequestObject) (api.DeleteWebhookSourceResponseObject, error) {
	if err := h.Settings.DeleteWebhookSource(ctx, req.SourceId); err != nil {
		return nil, err
	}
	return api.DeleteWebhookSource204Response{}, nil
}

func (h *handler) receiveWebhookSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sourceId")
	if !settings.ValidWebhookSourceID(id) {
		http.NotFound(w, r)
		return
	}
	config, err := h.Settings.WebhookSource(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("load webhook source", "source", id, "err", err)
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
	rule := webhook.Rule{TaskAgentID: config.TaskAgentID.String, HoldUnmatched: config.HoldUnmatched == 1}
	_, err = webhook.Accept(r.Context(), h.WorkTasks, id, []byte(config.Secret), r.Header.Get("X-Dex-Delivery"), r.Header.Get("X-Dex-Signature"), body, rule)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, webhook.ErrSignature):
		http.Error(w, "invalid signature", http.StatusUnauthorized)
	case errors.Is(err, webhook.ErrPayload), errors.Is(err, worktask.ErrInvalid):
		http.Error(w, "invalid delivery", http.StatusBadRequest)
	default:
		slog.Error("store webhook delivery", "source", id, "err", err)
		http.Error(w, "cannot accept delivery", http.StatusServiceUnavailable)
	}
}
