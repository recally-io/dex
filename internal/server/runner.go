package server

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/execenv"
	"github.com/recally-io/dex/internal/execenv/runnerproto"
)

func (h *handler) IssueRunnerCode(ctx context.Context, req api.IssueRunnerCodeRequestObject) (api.IssueRunnerCodeResponseObject, error) {
	if h.Token == "" {
		return api.IssueRunnerCodedefaultJSONResponse{StatusCode: http.StatusForbidden, Body: api.Error{Error: "Configure a Dex access token before pairing a runner."}}, nil
	}
	code, err := h.Envs.IssueRunnerCode(ctx, req.EnvironmentId)
	if errors.Is(err, execenv.ErrRunnerConflict) {
		return api.IssueRunnerCodedefaultJSONResponse{StatusCode: http.StatusConflict, Body: api.Error{Error: err.Error()}}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.IssueRunnerCode200JSONResponse{Code: code}, nil
}

func (h *handler) RevokeRunner(ctx context.Context, req api.RevokeRunnerRequestObject) (api.RevokeRunnerResponseObject, error) {
	if h.Token == "" {
		return api.RevokeRunnerdefaultJSONResponse{StatusCode: http.StatusForbidden, Body: api.Error{Error: "Configure a Dex access token before managing a runner."}}, nil
	}
	if err := h.Envs.RevokeRunner(ctx, req.EnvironmentId); err != nil {
		return nil, err
	}
	return api.RevokeRunner200JSONResponse(ok), nil
}

func (h *handler) runnerRoutes(admin http.Handler) http.Handler {
	if h.Envs == nil || h.Token == "" {
		return admin
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/runner/{id}/pair", h.pairRunner)
	mux.HandleFunc("GET /api/runner/{id}/connect", h.connectRunner)
	mux.Handle("/", admin)
	return mux
}

func runnerBearer(r *http.Request) string {
	credential, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || len(credential) < 26 || len(credential) > 128 || r.Header.Get("Origin") != "" {
		return ""
	}
	return credential
}

func (h *handler) pairRunner(w http.ResponseWriter, r *http.Request) {
	code := runnerBearer(r)
	if code == "" {
		writeError(w, http.StatusUnauthorized, "runner pairing code is required")
		return
	}
	token, err := h.Envs.PairRunner(r.Context(), r.PathValue("id"), code)
	if errors.Is(err, execenv.ErrRunnerCredential) {
		writeError(w, http.StatusUnauthorized, "runner pairing code is invalid or expired")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "pairing is temporarily unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, struct {
		Credential string `json:"credential"`
	}{Credential: token})
}

func (h *handler) connectRunner(w http.ResponseWriter, r *http.Request) {
	token := runnerBearer(r)
	id := r.PathValue("id")
	if token == "" || h.Envs.VerifyRunner(r.Context(), id, token) != nil {
		writeError(w, http.StatusUnauthorized, "runner credential is invalid")
		return
	}
	if !slices.Contains(strings.FieldsFunc(r.Header.Get("Sec-WebSocket-Protocol"), func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }), runnerproto.Version) {
		writeError(w, http.StatusUpgradeRequired, "runner protocol is incompatible")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{runnerproto.Version}})
	if err != nil {
		return
	}
	_ = h.Envs.AcceptRunner(r.Context(), id, token, conn)
}
