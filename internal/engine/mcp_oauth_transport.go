package engine

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MichaelKinsy/PiG/mcp/oauth"
	"github.com/recally-io/dex/internal/vault"
)

var errMCPAuthorize = errors.New("authorize this MCP connection in Integrations")
var errMCPReauthorize = errors.New("reauthorize this MCP connection in Integrations")

type mcpOAuthConnection struct {
	store   *MCPStore
	vault   *vault.Store
	entry   serverEntry
	maskMu  sync.Mutex
	current []string
	masker  *strings.Replacer
}

func (c *mcpOAuthConnection) remember(g *mcpGrant) {
	c.maskMu.Lock()
	defer c.maskMu.Unlock()
	values := []string{g.Tokens.AccessToken, g.Tokens.RefreshToken, g.Client.ClientSecret}
	if slices.Equal(c.current, values) {
		return
	}
	// Keep the previous values for replies that arrive during a refresh.
	secrets := slices.Concat(values, c.current)
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	pairs := make([]string, 0, 2*len(secrets))
	for _, value := range secrets {
		if value != "" {
			pairs = append(pairs, value, "[hidden]")
		}
	}
	c.current, c.masker = values, strings.NewReplacer(pairs...)
}

func (c *mcpOAuthConnection) mask(text string) string {
	c.maskMu.Lock()
	masker := c.masker
	c.maskMu.Unlock()
	if masker != nil {
		return masker.Replace(text)
	}
	return text
}

func (c *mcpOAuthConnection) maskJSON(value any) any {
	switch v := value.(type) {
	case string:
		return c.mask(v)
	case []any:
		for i, child := range v {
			v[i] = c.maskJSON(child)
		}
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			out[c.mask(key)] = c.maskJSON(child)
		}
		return out
	}
	return value
}

func (c *mcpOAuthConnection) token(ctx context.Context, rejected, challenge string, invalidate bool) (string, error) {
	key := c.entry.grantKey()
	lock := &c.store.oauthLocks[key[0]%64]
	lock.Lock()
	defer lock.Unlock()
	g, err := c.entry.loadGrant(c.vault)
	if err != nil {
		return "", &privateError{message: "cannot load the encrypted MCP grant", cause: err}
	}
	if g == nil {
		return "", errMCPAuthorize
	}
	if g.Tokens == nil {
		return "", errMCPReauthorize
	}
	if rejected != "" && rejected != g.Tokens.AccessToken {
		// Another request has already replaced the rejected token.
		if invalidate {
			return "", errMCPReauthorize
		}
		rejected = ""
	}
	if !invalidate && rejected == "" && !g.expired() {
		c.remember(g)
		return g.Tokens.AccessToken, nil
	}
	if !invalidate && g.Tokens.RefreshToken != "" {
		ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		options, err := g.tokenOptions(c.entry, c.vault)
		var tokens *oauth.OAuthTokens
		if err == nil {
			tokens, err = oauth.RefreshAuthorization(ctx, g.Metadata.Issuer, oauth.RefreshAuthorizationOptions{TokenRequestOptions: options, RefreshToken: g.Tokens.RefreshToken})
		}
		if err == nil {
			if err := g.saveTokens(tokens); err != nil {
				return "", err
			}
			if err := c.entry.saveGrant(c.vault, g); err != nil {
				return "", err
			}
			c.remember(g)
			return tokens.AccessToken, nil
		}
		e, isOAuth := errors.AsType[*oauth.OAuthError](err)
		_, missingSecret := errors.AsType[*vault.MissingError](err)
		if !missingSecret && (!isOAuth || !terminalMCPRefreshError(e.Code)) {
			return "", &privateError{message: "MCP token refresh failed; try again", cause: err}
		}
	}
	g.Tokens = nil
	g.Expires = time.Time{}
	g.Challenge = challenge
	if err := c.entry.saveGrant(c.vault, g); err != nil {
		return "", err
	}
	return "", errMCPReauthorize
}

func terminalMCPRefreshError(code string) bool {
	switch code {
	case "invalid_grant", "invalid_client", "unsupported_grant_type", "bad_refresh_token", "incorrect_client_credentials":
		return true
	default:
		return false
	}
}

// Do binds bearer credentials to the configured MCP endpoint. Retry once
// after a 401, which must mean that the server did not run the request.
func (c *mcpOAuthConnection) Do(req *http.Request) (*http.Response, error) {
	endpoint, err := mcpOAuthURL(c.entry.config.URL)
	if err != nil {
		return nil, err
	}
	path := endpoint.EscapedPath()
	if path == "" {
		path = "/"
	}
	if !mcpSameOrigin(req.URL, endpoint) || req.URL.EscapedPath() != path || req.URL.RawQuery != endpoint.RawQuery {
		return nil, errors.New("MCP request is not for the authorized endpoint")
	}
	ctx := req.Context()
	req = req.Clone(ctx)
	if req.Method == http.MethodDelete {
		// Close is best effort. It must not refresh or invalidate the grant for other sessions.
		c.maskMu.Lock()
		if len(c.current) != 0 {
			req.Header.Set("Authorization", "Bearer "+c.current[0])
		}
		c.maskMu.Unlock()
		return mcpOAuthDirect.Do(req) //nolint:bodyclose,gosec // The SDK closes the body. The request must match the configured endpoint.
	}
	token, err := c.token(ctx, "", "", false)
	if err != nil {
		return nil, err
	}
	for i := range 2 {
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := mcpOAuthDirect.Do(req) //nolint:bodyclose,gosec // The SDK closes successful bodies. The request must match the configured endpoint.
		if err != nil {
			return nil, &privateError{message: "MCP request failed", cause: err}
		}
		challenge := mcpOAuthChallenge(response)
		insufficient := response.StatusCode == http.StatusForbidden && challenge.Error == "insufficient_scope"
		if response.StatusCode != http.StatusUnauthorized && !insufficient {
			return response, nil
		}
		_ = response.Body.Close()
		token, err = c.token(ctx, token, challenge.Scope, insufficient || i > 0)
		if err != nil {
			return nil, err
		}
		if req.Body != nil && req.Body != http.NoBody {
			if req.GetBody == nil {
				return nil, errors.New("cannot retry the MCP request after refresh")
			}
			req.Body, err = req.GetBody()
			if err != nil {
				return nil, err
			}
		}
	}
	return nil, errMCPReauthorize
}

func (e serverEntry) oauthStatus(v *vault.Store) string {
	if e.config.Disabled || e.config.OAuth == nil {
		return ""
	}
	grant, err := e.loadGrant(v)
	if err != nil {
		return "reauthorize"
	}
	if grant == nil {
		return "authorize"
	}
	if grant.Tokens == nil || (grant.expired() && grant.Tokens.RefreshToken == "") {
		return "reauthorize"
	}
	return "authorized"
}
