package webhook

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
	"regexp"

	"github.com/recally-io/dex/internal/plugin"
	tasksvc "github.com/recally-io/dex/internal/task"
	"github.com/recally-io/dex/plugins/webhook/api"
	"github.com/recally-io/dex/plugins/webhook/store"
)

var (
	errInvalid = errors.New("invalid request")
	validID    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

// New makes the Webhook plugin: signed sources that send normalized Task events.
func New(deps plugin.Deps) plugin.Plugin {
	s := &service{q: store.New(deps.DB), tasks: deps.Tasks}
	hooks := http.NewServeMux()
	hooks.HandleFunc("POST /hooks/webhook/{sourceId}", s.receive)
	routes := http.NewServeMux()
	api.HandlerWithOptions(api.NewStrictHandlerWithOptions(s, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadRequest, err.Error())
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			status := http.StatusInternalServerError
			if errors.Is(err, errInvalid) {
				status = http.StatusBadRequest
			} else {
				slog.Error("webhook plugin api", "err", err)
			}
			writeError(w, status, err.Error())
		},
	}), api.StdHTTPServerOptions{BaseRouter: routes})
	return plugin.Plugin{Name: "webhook", Hooks: hooks, HookLimit: MaxPayload, API: routes, Migrations: Migrations}
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
	id := r.PathValue("sourceId")
	if !validID.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	source, err := s.q.GetWebhookSource(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("load Webhook source", "source", id, "err", err)
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
	_, err = Accept(r.Context(), s.tasks, id, []byte(source.Secret), r.Header.Get("X-Dex-Delivery"), r.Header.Get("X-Dex-Signature"), body, Rule{TaskBotID: source.TaskBotID.String, HoldUnmatched: source.HoldUnmatched == 1})
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrSignature):
		http.Error(w, "invalid signature", http.StatusUnauthorized)
	case errors.Is(err, ErrPayload), errors.Is(err, tasksvc.ErrInvalid):
		http.Error(w, "invalid delivery", http.StatusBadRequest)
	default:
		slog.Error("store Webhook delivery", "source", id, "err", err)
		http.Error(w, "cannot accept delivery", http.StatusServiceUnavailable)
	}
}

// SaveWebhookSource replaces the write-only secret and the rule for unmatched events of one source.
func (s *service) SaveWebhookSource(ctx context.Context, req api.SaveWebhookSourceRequestObject) (api.SaveWebhookSourceResponseObject, error) {
	if req.Body == nil || req.Body.Secret == nil || !validID.MatchString(req.SourceId) || len(*req.Body.Secret) < 32 || len(*req.Body.Secret) > 256 {
		return nil, fmt.Errorf("%w: use a source ID of lowercase letters, digits, and dashes, and a secret of 32 to 256 bytes", errInvalid)
	}
	bot := *cmp.Or(req.Body.TaskBotId, new(""))
	hold := int64(0)
	if req.Body.HoldUnmatched != nil && *req.Body.HoldUnmatched {
		hold = 1
	}
	n, err := s.q.SaveWebhookSource(ctx, store.SaveWebhookSourceParams{ID: req.SourceId, Secret: *req.Body.Secret, TaskBotID: sql.NullString{String: bot, Valid: bot != ""}, HoldUnmatched: hold})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("%w: select an active bot for new Tasks", errInvalid)
	}
	return api.SaveWebhookSource204Response{}, nil
}

// DeleteWebhookSource removes the secret and disables new deliveries. Recorded events stay.
func (s *service) DeleteWebhookSource(ctx context.Context, req api.DeleteWebhookSourceRequestObject) (api.DeleteWebhookSourceResponseObject, error) {
	if !validID.MatchString(req.SourceId) {
		return nil, errInvalid
	}
	if err := s.q.DeleteWebhookSource(ctx, req.SourceId); err != nil {
		return nil, err
	}
	return api.DeleteWebhookSource204Response{}, nil
}
