package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/recally-io/dex/internal/api"
	"github.com/recally-io/dex/internal/engine"
)

const mcpCallbackPath = "/mcp/oauth/callback"
const mcpOAuthTTL = 10 * time.Minute

type mcpOAuthRequestKey struct{}
type mcpOAuthOrigin struct{ origin, host string }

type mcpOAuthRequests struct {
	mu      sync.Mutex
	pending map[string]pendingMCPGrant
}

type pendingMCPGrant struct {
	binding string
	host    string
	botID   string
	secure  bool
	expires time.Time
	grant   *engine.MCPAuthorization
}

func (h *handler) StartMcpOAuth(ctx context.Context, req api.StartMcpOAuthRequestObject) (api.StartMcpOAuthResponseObject, error) {
	if req.Body == nil {
		return nil, errInvalid
	}
	link, cookie, err := h.startMcpOAuth(ctx, botID(ctx), string(req.Body.Scope), req.Name)
	if err != nil {
		return nil, err
	}
	out := api.StartMcpOAuth200JSONResponse{}
	out.Body.AuthorizationUrl = link
	out.Headers.SetCookie = &cookie
	return out, nil
}

func (h *handler) StartSharedMcpOAuth(ctx context.Context, req api.StartSharedMcpOAuthRequestObject) (api.StartSharedMcpOAuthResponseObject, error) {
	link, cookie, err := h.startMcpOAuth(ctx, "", "dex", req.Name)
	if err != nil {
		return nil, err
	}
	out := api.StartSharedMcpOAuth200JSONResponse{}
	out.Body.AuthorizationUrl = link
	out.Headers.SetCookie = &cookie
	return out, nil
}

func (h *handler) startMcpOAuth(ctx context.Context, id, scope, name string) (link, cookie string, err error) {
	// Only a same-origin browser can start an attempt. Do not trust forwarded headers.
	request, ok := ctx.Value(mcpOAuthRequestKey{}).(mcpOAuthOrigin)
	if !ok {
		return "", "", errInvalid
	}
	origin, err := url.Parse(request.origin)
	if err != nil || origin.Hostname() == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" || !strings.EqualFold(origin.Host, request.host) {
		return "", "", fmt.Errorf("%w: start MCP authorization from the Dex web page", errInvalid)
	}
	secure := origin.Scheme == "https"
	loopback := origin.Hostname() == "127.0.0.1" || origin.Hostname() == "::1" || origin.Hostname() == "localhost"
	if !secure && (origin.Scheme != "http" || !loopback) {
		return "", "", fmt.Errorf("%w: open Dex over HTTPS or on localhost to authorize MCP", errInvalid)
	}
	state := rand.Text()
	requests := &h.mcpOAuth
	requests.mu.Lock()
	maps.DeleteFunc(requests.pending, func(_ string, p pendingMCPGrant) bool { return time.Now().After(p.expires) })
	if len(requests.pending) >= 128 {
		requests.mu.Unlock()
		return "", "", fmt.Errorf("%w: too many pending MCP authorization attempts", errInvalid)
	}
	if requests.pending == nil {
		requests.pending = make(map[string]pendingMCPGrant)
	}
	// Reserve the slot before discovery. It cannot complete until grant is set.
	requests.pending[state] = pendingMCPGrant{expires: time.Now().Add(mcpOAuthTTL)}
	requests.mu.Unlock()
	link, grant, err := h.Engine.StartMCPAuthorization(ctx, id, scope, name, origin.String()+mcpCallbackPath, state)
	requests.mu.Lock()
	defer requests.mu.Unlock()
	if err != nil {
		delete(requests.pending, state)
		return "", "", err
	}
	p := pendingMCPGrant{binding: rand.Text(), host: origin.Host, secure: secure, botID: id, expires: time.Now().Add(mcpOAuthTTL), grant: grant}
	requests.pending[state] = p
	return link, mcpOAuthCookie(state, p.binding, p.secure, int(mcpOAuthTTL.Seconds())).String(), nil
}

func mcpOAuthCookie(state, value string, secure bool, age int) *http.Cookie {
	//nolint:gosec // HTTPS uses Secure. The only other allowed origin is HTTP on loopback.
	return &http.Cookie{Name: "dex_mcp_oauth_" + state, Value: value, Path: mcpCallbackPath, MaxAge: age, Secure: secure, HttpOnly: true, SameSite: http.SameSiteLaxMode}
}

func (h *handler) finishMcpOAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["state"]) != 1 {
		writeError(w, 400, "invalid MCP authorization state")
		return
	}
	state := query.Get("state")
	requests := &h.mcpOAuth
	requests.mu.Lock()
	p, ok := requests.pending[state]
	cookie, cookieErr := r.Cookie(mcpOAuthCookie(state, "", false, 0).Name)
	valid := ok && p.grant != nil && time.Now().Before(p.expires) && strings.EqualFold(r.Host, p.host) && cookieErr == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(p.binding)) == 1
	if valid {
		delete(requests.pending, state)
	}
	requests.mu.Unlock()
	if !valid {
		writeError(w, 400, "invalid or expired MCP authorization state; start again from Integrations")
		return
	}
	http.SetCookie(w, mcpOAuthCookie(state, "", p.secure, -1))
	if query.Has("error") || len(query["code"]) != 1 || len(query["iss"]) > 1 {
		writeError(w, 400, "MCP authorization did not complete; return to Integrations and try again")
		return
	}
	var iss *string
	if query.Has("iss") {
		iss = new(query.Get("iss"))
	}
	destination := "/integrations?section=mcp"
	if p.botID != "" {
		if archived, err := h.Bots.Bot(r.Context(), p.botID); err != nil || archived.ArchivedAt != 0 {
			writeError(w, 400, "the MCP authorization target is no longer available")
			return
		}
		destination = "/b/" + url.PathEscape(p.botID) + "/integrations"
	}
	if err := h.Engine.FinishMCPAuthorization(r.Context(), p.grant, query.Get("code"), iss); err != nil {
		writeError(w, 400, "MCP authorization failed; return to Integrations and try again")
		return
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
}
