package email

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
	"time"
	"uuid"

	"github.com/recally-io/dex/internal/plugin"
	"github.com/recally-io/dex/internal/worktask"
	"github.com/recally-io/dex/plugins/email/api"
	"github.com/recally-io/dex/plugins/email/store"
)

var (
	errInvalid  = errors.New("invalid request")
	errNotFound = errors.New("email source not found")
	validID     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

// New makes the email plugin: signed sources that post raw messages of one address.
func New(deps plugin.Deps) plugin.Plugin {
	s := &service{q: store.New(deps.DB), tasks: deps.WorkTasks}
	hooks := http.NewServeMux()
	hooks.HandleFunc("POST /hooks/email/{sourceId}", s.receive)
	routes := http.NewServeMux()
	api.HandlerWithOptions(api.NewStrictHandlerWithOptions(s, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, http.StatusBadRequest, err.Error())
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			status := http.StatusInternalServerError
			switch {
			case errors.Is(err, errInvalid):
				status = http.StatusBadRequest
			case errors.Is(err, errNotFound):
				status = http.StatusNotFound
			default:
				slog.Error("email plugin api", "err", err)
			}
			writeError(w, status, err.Error())
		},
	}), api.StdHTTPServerOptions{BaseRouter: routes})
	pull := &puller{q: s.q, tasks: s.tasks}
	return plugin.Plugin{
		Name: "email", Hooks: hooks, HookLimit: MaxPayload, API: routes, Migrations: Migrations,
		Jobs: []plugin.Job{{Name: "imap-pull", Every: time.Minute, Run: pull.Check}},
	}
}

type service struct {
	q     *store.Queries
	tasks *worktask.Service
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
	source, err := s.q.GetEmailSource(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("load email source", "source", id, "err", err)
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
	_, err = Accept(r.Context(), s.tasks, Source{ID: id, Secret: []byte(source.Secret), TaskAgentID: source.TaskAgentID.String, HoldUnmatched: source.HoldUnmatched == 1}, r.Header.Get("X-Dex-Delivery"), r.Header.Get("X-Dex-Signature"), body)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrSignature):
		http.Error(w, "invalid signature", http.StatusUnauthorized)
	case errors.Is(err, ErrPayload), errors.Is(err, worktask.ErrInvalid):
		http.Error(w, "invalid delivery", http.StatusBadRequest)
	default:
		slog.Error("store email delivery", "source", id, "err", err)
		http.Error(w, "cannot accept delivery", http.StatusServiceUnavailable)
	}
}

// SaveEmailSource replaces the write-only secret and the rule for unmatched messages of one source.
func (s *service) SaveEmailSource(ctx context.Context, req api.SaveEmailSourceRequestObject) (api.SaveEmailSourceResponseObject, error) {
	if req.Body == nil || req.Body.Secret == nil || !validID.MatchString(req.SourceId) || len(*req.Body.Secret) < 32 || len(*req.Body.Secret) > 256 {
		return nil, fmt.Errorf("%w: use a source ID of lowercase letters, digits, and dashes, and a secret of 32 to 256 bytes", errInvalid)
	}
	agent := *cmp.Or(req.Body.TaskAgentId, new(""))
	hold := int64(0)
	if req.Body.HoldUnmatched != nil && *req.Body.HoldUnmatched {
		hold = 1
	}
	n, err := s.q.SaveEmailSource(ctx, store.SaveEmailSourceParams{ID: req.SourceId, Secret: *req.Body.Secret, TaskAgentID: sql.NullString{String: agent, Valid: agent != ""}, HoldUnmatched: hold})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("%w: select an active agent for new threads", errInvalid)
	}
	return api.SaveEmailSource204Response{}, nil
}

// DeleteEmailSource removes the secret and disables new deliveries. Recorded events stay.
func (s *service) DeleteEmailSource(ctx context.Context, req api.DeleteEmailSourceRequestObject) (api.DeleteEmailSourceResponseObject, error) {
	if !validID.MatchString(req.SourceId) {
		return nil, errInvalid
	}
	if err := s.q.DeleteEmailSource(ctx, req.SourceId); err != nil {
		return nil, err
	}
	return api.DeleteEmailSource204Response{}, nil
}

// SaveEmailIMAPPull replaces the IMAP folder of a source and its write-only password.
func (s *service) SaveEmailIMAPPull(ctx context.Context, req api.SaveEmailIMAPPullRequestObject) (api.SaveEmailIMAPPullResponseObject, error) {
	if req.Body == nil || req.Body.Password == nil || !validID.MatchString(req.SourceId) {
		return nil, errInvalid
	}
	b := req.Body
	password := *b.Password
	folder := *cmp.Or(b.Folder, new("INBOX"))
	if _, err := parseIMAPAddress(b.Address); err != nil {
		return nil, fmt.Errorf("%w: %w", errInvalid, err)
	}
	if b.Username == "" || len(b.Username) > 320 || password == "" || len(password) > 1024 || folder == "" || len(folder) > 255 {
		return nil, fmt.Errorf("%w: give a username, a password, and a folder", errInvalid)
	}
	n, err := s.q.SaveEmailIMAPPull(ctx, store.SaveEmailIMAPPullParams{SourceID: req.SourceId, Address: b.Address, Username: b.Username, Password: password, Folder: folder, ConfigurationID: uuid.NewV7().String()})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errNotFound
	}
	return api.SaveEmailIMAPPull204Response{}, nil
}

// DeleteEmailIMAPPull stops reading the folder. Deleting the source also deletes its Pull.
func (s *service) DeleteEmailIMAPPull(ctx context.Context, req api.DeleteEmailIMAPPullRequestObject) (api.DeleteEmailIMAPPullResponseObject, error) {
	if !validID.MatchString(req.SourceId) {
		return nil, errInvalid
	}
	if err := s.q.DeleteEmailIMAPPull(ctx, req.SourceId); err != nil {
		return nil, err
	}
	return api.DeleteEmailIMAPPull204Response{}, nil
}
