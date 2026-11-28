package server

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/host/update"
	"github.com/recally-io/dex/internal/runner"
	"github.com/recally-io/dex/internal/runner/outbound"
	"golang.org/x/mod/semver"
)

func (h *handler) IssueRunnerCode(ctx context.Context, req api.IssueRunnerCodeRequestObject) (api.IssueRunnerCodeResponseObject, error) {
	if h.Runners == nil {
		return api.IssueRunnerCodedefaultJSONResponse{StatusCode: http.StatusServiceUnavailable, Body: api.Error{Error: "Remote machines are unavailable in this build."}}, nil
	}
	code, err := h.Runners.IssueCode(ctx, req.RunnerId)
	if errors.Is(err, runner.ErrRunnerConflict) {
		return api.IssueRunnerCodedefaultJSONResponse{StatusCode: http.StatusConflict, Body: api.Error{Error: err.Error()}}, nil
	}
	if err != nil {
		return nil, err
	}
	return api.IssueRunnerCode200JSONResponse{Code: code}, nil
}

func (h *handler) RevokeRunner(ctx context.Context, req api.RevokeRunnerRequestObject) (api.RevokeRunnerResponseObject, error) {
	if h.Runners == nil {
		return api.RevokeRunnerdefaultJSONResponse{StatusCode: http.StatusServiceUnavailable, Body: api.Error{Error: "Remote machines are unavailable in this build."}}, nil
	}
	if err := h.Runners.Revoke(ctx, req.RunnerId); err != nil {
		return nil, err
	}
	h.runnerChanged(ctx, req.RunnerId)
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
	if h.Runners == nil {
		writeError(w, http.StatusServiceUnavailable, "remote machines are unavailable in this build")
		return
	}
	token, err := h.Runners.Enroll(r.Context(), r.PathValue("id"), code)
	switch {
	case errors.Is(err, runner.ErrRunnerCredential):
		writeError(w, http.StatusUnauthorized, "the enrollment code is invalid or expired")
		return
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, "enrollment is temporarily unavailable")
		return
	}
	h.runnerChanged(r.Context(), r.PathValue("id"))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": token, "protocol": outbound.Version})
}

// machineConnect serves the session of one machine. The machine sends its token in the
// Authorization header, and the socket protocol version in the WebSocket subprotocol.
func (h *handler) machineConnect(w http.ResponseWriter, r *http.Request) {
	id, token := r.PathValue("id"), machineBearer(r)
	if h.Runners == nil || token == "" || h.Runners.Verify(r.Context(), id, token) != nil {
		writeError(w, http.StatusUnauthorized, "the machine token is not valid; install the machine again")
		return
	}
	if h.Version != "" {
		w.Header().Set("X-Dex-Version", h.Version)
	}
	if !slices.Contains(strings.FieldsFunc(r.Header.Get("Sec-WebSocket-Protocol"), func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }), outbound.Version) {
		writeError(w, http.StatusUpgradeRequired, "the machine protocol is incompatible")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{outbound.Version}})
	if err != nil {
		return
	}
	_ = h.Runners.Accept(r.Context(), id, token, conn)
}

// validRunnerReleaseFile matches valid dex-agent release archive filenames for a specific server tag.
func validRunnerReleaseFile(tag, name string) bool {
	if name == "checksums.txt" {
		return true
	}
	prefix := "dex-agent_" + tag + "_"
	rest, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return false
	}
	switch rest {
	case "linux_amd64.tar.gz", "linux_arm64.tar.gz", "darwin_amd64.tar.gz", "darwin_arm64.tar.gz":
		return true
	default:
		return false
	}
}

// machineRunnerRelease serves checksums.txt and dex-agent archives matching this Dex server's
// running version to enrolled machines, caching downloaded release assets on disk.
func (h *handler) machineRunnerRelease(w http.ResponseWriter, r *http.Request) {
	id, token := r.PathValue("id"), machineBearer(r)
	if h.Runners == nil || token == "" || h.Runners.Verify(r.Context(), id, token) != nil {
		writeError(w, http.StatusUnauthorized, "the machine token is not valid; install the machine again")
		return
	}
	file := r.PathValue("file")
	tag := h.Version
	if !semver.IsValid(tag) {
		writeError(w, http.StatusServiceUnavailable, "this Dex build has no matching machine archive")
		return
	}
	if !validRunnerReleaseFile(tag, file) {
		http.NotFound(w, r)
		return
	}
	cached, err := h.openRunnerRelease(r.Context(), tag, file)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("fetch runner release asset: %v", err))
		return
	}
	defer cached.Close()
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, file, time.Time{}, cached)
}

func (h *handler) openRunnerRelease(ctx context.Context, tag, name string) (*os.File, error) {
	// Fill one cache entry at a time. Open files can be served concurrently.
	h.runnerReleaseMu.Lock()
	defer h.runnerReleaseMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cacheDir := filepath.Join(h.DataDir, "cache", "machine-release", tag)
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(cacheDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if f, err := root.Open(name); err == nil {
		return f, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	const manifest = "checksums.txt"
	var checksums []byte
	if name != manifest {
		checksums, err = root.ReadFile(manifest)
		if errors.Is(err, os.ErrNotExist) {
			if err := h.cacheRunnerAsset(ctx, root, tag, manifest, nil); err != nil {
				return nil, err
			}
			checksums, err = root.ReadFile(manifest)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := h.cacheRunnerAsset(ctx, root, tag, name, checksums); err != nil {
		return nil, err
	}
	return root.Open(name)
}

func (h *handler) cacheRunnerAsset(ctx context.Context, root *os.Root, tag, name string, checksums []byte) error {
	tmp, err := os.CreateTemp(root.Name(), ".fetch-*")
	if err != nil {
		return err
	}
	tmpName := filepath.Base(tmp.Name())
	defer func() {
		_ = tmp.Close()
		_ = root.Remove(tmpName)
	}()
	if err := update.DownloadReleaseAsset(ctx, h.ReleaseBase, tag, name, tmp); err != nil {
		return err
	}
	if name != "checksums.txt" {
		if _, err := tmp.Seek(0, 0); err != nil {
			return err
		}
		if err := update.VerifyChecksum(checksums, tmp, name); err != nil {
			// A retry must fetch fresh checksums if GitHub replaced a release asset.
			return errors.Join(err, root.Remove("checksums.txt"))
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return root.Rename(tmpName, name)
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
func (h *handler) machineFile(w http.ResponseWriter, _ *http.Request) {
	tag := h.Version
	switch {
	case tag == "dev" || tag == "":
		tag = "v0.0.0-dev"
	case !semver.IsValid(tag):
		writeError(w, http.StatusServiceUnavailable, "this Dex build has no matching machine archive")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(bytes.ReplaceAll(machineInstaller, []byte("__DEX_MACHINE_TAG__"), []byte(tag)))
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

//go:embed install-machine.sh
var machineInstaller []byte
