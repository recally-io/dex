package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/execenv"
	"github.com/recally-io/dex/internal/execenv/runner"
)

func (h *handler) IssueRunnerCode(ctx context.Context, req api.IssueRunnerCodeRequestObject) (api.IssueRunnerCodeResponseObject, error) {
	if h.Envs == nil {
		return api.IssueRunnerCodedefaultJSONResponse{StatusCode: http.StatusServiceUnavailable, Body: api.Error{Error: "Remote machines are unavailable in this build."}}, nil
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
	if h.Envs == nil {
		return api.RevokeRunnerdefaultJSONResponse{StatusCode: http.StatusServiceUnavailable, Body: api.Error{Error: "Remote machines are unavailable in this build."}}, nil
	}
	if err := h.Envs.RevokeRunner(ctx, req.EnvironmentId); err != nil {
		return nil, err
	}
	return api.RevokeRunner200JSONResponse(ok), nil
}

// machinePeer returns the identity of the machine certificate on a machine listener request.
// The machine listener asks for a client certificate, and the install command uses the same
// key for its first and later connections.
func machinePeer(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || r.Header.Get("Origin") != "" {
		return ""
	}
	cert := r.TLS.PeerCertificates[0]
	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return ""
	}
	key := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "mtls:" + base64.StdEncoding.EncodeToString(key[:])
}

func (h *handler) pairRunner(w http.ResponseWriter, r *http.Request) {
	code := runnerBearer(r)
	if code == "" {
		writeError(w, http.StatusUnauthorized, "the one-time enrollment code is required")
		return
	}
	identity := machinePeer(r)
	if identity == "" {
		writeError(w, http.StatusUnauthorized, "the machine client certificate is required")
		return
	}
	if err := h.Envs.EnrollRunner(r.Context(), r.PathValue("id"), code, identity); errors.Is(err, execenv.ErrRunnerCredential) {
		writeError(w, http.StatusUnauthorized, "the enrollment code is invalid or expired")
		return
	} else if err != nil {
		writeError(w, http.StatusServiceUnavailable, "enrollment is temporarily unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) connectRunner(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	identity := machinePeer(r)
	if identity == "" || h.Envs.VerifyRunner(r.Context(), id, identity) != nil {
		writeError(w, http.StatusUnauthorized, "the machine certificate is not enrolled; install the machine again")
		return
	}
	if !slices.Contains(strings.FieldsFunc(r.Header.Get("Sec-WebSocket-Protocol"), func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }), runner.Version) {
		writeError(w, http.StatusUpgradeRequired, "the machine protocol is incompatible")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{runner.Version}})
	if err != nil {
		return
	}
	_ = h.Envs.AcceptRunner(r.Context(), id, identity, conn)
}

// runnerBearer reads the one-time enrollment code. It refuses a browser request.
func runnerBearer(r *http.Request) string {
	code, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || len(code) < 26 || len(code) > 128 || r.Header.Get("Origin") != "" {
		return ""
	}
	return code
}
