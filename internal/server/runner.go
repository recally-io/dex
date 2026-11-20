package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

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

// machinePair trades a one-time code for the machine token. The machine sends the code in the
// Authorization header, so the code stays out of every proxy log.
func (h *handler) machinePair(w http.ResponseWriter, r *http.Request) {
	code := machineBearer(r)
	if code == "" {
		writeError(w, http.StatusUnauthorized, "the one-time enrollment code is required")
		return
	}
	if h.Envs == nil {
		writeError(w, http.StatusServiceUnavailable, "remote machines are unavailable in this build")
		return
	}
	token, err := h.Envs.EnrollRunner(r.Context(), r.PathValue("id"), code)
	switch {
	case errors.Is(err, execenv.ErrRunnerCredential):
		writeError(w, http.StatusUnauthorized, "the enrollment code is invalid or expired")
		return
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, "enrollment is temporarily unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": token, "protocol": runner.Version})
}

// machineConnect serves the session of one machine. The machine sends its token in the
// Authorization header, and the socket protocol version in the WebSocket subprotocol.
func (h *handler) machineConnect(w http.ResponseWriter, r *http.Request) {
	id, token := r.PathValue("id"), machineBearer(r)
	if h.Envs == nil || token == "" || h.Envs.VerifyRunner(r.Context(), id, token) != nil {
		writeError(w, http.StatusUnauthorized, "the machine token is not valid; install the machine again")
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
	_ = h.Envs.AcceptRunner(r.Context(), id, token, conn)
}

// machineBearer reads the machine credential. It refuses a browser request, because a page in
// the browser can send an Origin header.
func machineBearer(r *http.Request) string {
	value, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || len(value) < 26 || len(value) > 128 || r.Header.Get("Origin") != "" {
		return ""
	}
	return value
}

// machineFile serves the installer and, for a development build, its own archives. These
// files are public: a machine reads them before it has a token.
func (h *handler) machineFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(machineInstaller)
}

// devArchive matches the names of the development archives, so no other file is readable.
var devArchive = regexp.MustCompile(`^dex-agent_v0\.0\.0-dev_(linux|darwin)_(amd64|arm64)\.tar\.gz$`)

// machineRelease serves the development archives. A release publishes them instead.
func (h *handler) machineRelease(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if name != "checksums.txt" && !devArchive.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	// The name is one of the fixed development archives, or the checksum file.
	http.ServeFile(w, r, filepath.Join(h.DataDir, "machine-release", name)) //nolint:gosec // See above.
}
