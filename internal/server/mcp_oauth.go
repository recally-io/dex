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
	agentID string
	secure  bool
	expires time.Time
	grant   *engine.MCPAuthorization
}

func (h *handler) StartMcpOAuth(ctx context.Context, req api.StartMcpOAuthRequestObject) (api.StartMcpOAuthResponseObject, error) {
	// Only a same-origin browser can start an attempt. Do not trust forwarded headers.
	request, ok := ctx.Value(mcpOAuthRequestKey{}).(mcpOAuthOrigin)
	if !ok || req.Body == nil {
		return nil, errInvalid
	}
	origin, err := url.Parse(request.origin)
	if err != nil || origin.Hostname() == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" || !strings.EqualFold(origin.Host, request.host) {
		return nil, fmt.Errorf("%w: start MCP authorization from the Dex web page", errInvalid)
	}
	secure := origin.Scheme == "https"
	loopback := origin.Hostname() == "127.0.0.1" || origin.Hostname() == "::1" || origin.Hostname() == "localhost"
	if !secure && (origin.Scheme != "http" || !loopback) {
		return nil, fmt.Errorf("%w: open Dex over HTTPS or on localhost to authorize MCP", errInvalid)
	}
	id := agentID(ctx)
	state := rand.Text()
	requests := &h.mcpOAuth
	requests.mu.Lock()
	maps.DeleteFunc(requests.pending, func(_ string, p pendingMCPGrant) bool { return time.Now().After(p.expires) })
	if len(requests.pending) >= 128 {
		requests.mu.Unlock()
		return nil, fmt.Errorf("%w: too many pending MCP authorization attempts", errInvalid)
	}
	if requests.pending == nil {
		requests.pending = make(map[string]pendingMCPGrant)
	}
	// Reserve the slot before discovery. It cannot complete until grant is set.
	requests.pending[state] = pendingMCPGrant{expires: time.Now().Add(mcpOAuthTTL)}
	requests.mu.Unlock()
	link, grant, err := h.Engine.StartMCPAuthorization(ctx, id, string(req.Body.Scope), req.Name, origin.String()+mcpCallbackPath, state)
	requests.mu.Lock()
	defer requests.mu.Unlock()
	if err != nil {
		delete(requests.pending, state)
		return nil, err
	}
	p := pendingMCPGrant{binding: rand.Text(), host: origin.Host, secure: secure, agentID: id, expires: time.Now().Add(mcpOAuthTTL), grant: grant}
	requests.pending[state] = p
	out := api.StartMcpOAuth200JSONResponse{}
	out.Body.AuthorizationUrl = link
	out.Headers.SetCookie = new(mcpOAuthCookie(state, p.binding, p.secure, int(mcpOAuthTTL.Seconds())).String())
	return out, nil
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
		writeError(w, 400, "invalid or expired MCP authorization state; start again from Tools")
		return
	}
	http.SetCookie(w, mcpOAuthCookie(state, "", p.secure, -1))
	if query.Has("error") || len(query["code"]) != 1 || len(query["iss"]) > 1 {
		writeError(w, 400, "MCP authorization did not complete; return to Tools and try again")
		return
	}
	var iss *string
	if query.Has("iss") {
		iss = new(query.Get("iss"))
	}
	if archived, err := h.Agents.Agent(r.Context(), p.agentID); err != nil || archived.ArchivedAt != 0 {
		writeError(w, 400, "the MCP authorization target is no longer available")
		return
	}
	if err := h.Engine.FinishMCPAuthorization(r.Context(), p.grant, query.Get("code"), iss); err != nil {
		writeError(w, 400, "MCP authorization failed; return to Tools and try again")
		return
	}
	http.Redirect(w, r, "/a/"+url.PathEscape(p.agentID)+"/extensions", http.StatusSeeOther)
}
