package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/MichaelKinsy/PiG/mcp"
	"github.com/MichaelKinsy/PiG/mcp/oauth"
	"github.com/recally-io/dex/internal/vault"
)

type mcpOAuthConfig struct {
	ClientID                string `json:"clientId,omitempty"`
	ClientSecret            string `json:"clientSecret,omitempty"`
	Scope                   string `json:"scope,omitempty"`
	TokenEndpointAuthMethod string `json:"tokenEndpointAuthMethod,omitempty"`
}

func validateMCPOAuth(c serverConfig) error {
	if c.OAuth == nil {
		return nil
	}
	if c.Type != "http" || len(c.Headers) != 0 {
		return fmt.Errorf("%w: OAuth requires HTTP without static headers", ErrInvalidInput)
	}
	if _, err := mcpOAuthURL(c.URL); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	if secret := c.OAuth.ClientSecret; secret != "" && (c.OAuth.ClientID == "" || !mcpOAuthSecret.MatchString(secret)) {
		return fmt.Errorf("%w: OAuth clientSecret must be a vault:NAME reference with a clientId", ErrInvalidInput)
	}
	if method := c.OAuth.TokenEndpointAuthMethod; method != "" && (c.OAuth.ClientSecret == "" || (method != "client_secret_basic" && method != "client_secret_post")) {
		return fmt.Errorf("%w: tokenEndpointAuthMethod requires a client secret and must be client_secret_basic or client_secret_post", ErrInvalidInput)
	}
	return nil
}

// MCPAuthorization is a pending browser grant. Keep it in memory until the
// callback consumes its state. It is never part of a declaration or tool result.
type MCPAuthorization struct {
	agentID  string
	entry    serverEntry
	grant    mcpGrant
	redirect string
	verifier string
}

type mcpGrant struct {
	Binding   string                            `json:"binding"`
	Resource  string                            `json:"resource"`
	Redirect  string                            `json:"redirect"`
	Metadata  oauth.AuthorizationServerMetadata `json:"metadata"`
	Client    oauth.OAuthClientInformation      `json:"client"`
	Tokens    *oauth.OAuthTokens                `json:"tokens,omitempty"`
	Expires   time.Time                         `json:"expires,omitzero"`
	Scope     string                            `json:"scope,omitempty"`
	Challenge string                            `json:"challenge,omitempty"`
}

func (e serverEntry) grantKey() [32]byte {
	// Dex has one owner per data directory. The vault and its key belong to that owner.
	return sha256.Sum256([]byte(e.scopeID + "\x00" + e.info.Name))
}

func (e serverEntry) grantBinding() string { return e.config.OAuthID + "\x00" + e.config.URL }

func (e serverEntry) loadGrant(v *vault.Store) (*mcpGrant, error) {
	raw, err := v.MCPGrant(e.grantKey())
	if err != nil || raw == "" {
		return nil, err
	}
	var grant mcpGrant
	if err := json.Unmarshal([]byte(raw), &grant); err != nil {
		return nil, errors.New("invalid encrypted MCP grant")
	}
	if grant.Binding != e.grantBinding() {
		return nil, nil
	}
	return &grant, nil
}

func (e serverEntry) saveGrant(v *vault.Store, g *mcpGrant) error {
	raw, err := json.Marshal(g)
	if err != nil {
		return errors.New("cannot encode MCP grant")
	}
	if err := v.SaveMCPGrant(e.grantKey(), string(raw)); err != nil {
		return &privateError{message: "cannot save encrypted MCP grant", cause: err}
	}
	return nil
}

func (g *mcpGrant) saveTokens(tokens *oauth.OAuthTokens) error {
	if !strings.EqualFold(tokens.TokenType, "Bearer") || tokens.AccessToken == "" || strings.ContainsAny(tokens.AccessToken, " \t\r\n") {
		return errors.New("the OAuth provider did not return a valid Bearer token")
	}
	// Dex does not use ID tokens.
	tokens.IDToken = ""
	g.Tokens = tokens
	if tokens.Scope != "" {
		g.Scope = tokens.Scope
	}
	g.Expires = time.Time{}
	if tokens.ExpiresIn != nil {
		const ceiling = 100 * 365 * 24 * time.Hour
		seconds := min(max(0, *tokens.ExpiresIn), ceiling.Seconds())
		g.Expires = time.Now().Add(time.Duration(seconds * float64(time.Second)))
	}
	g.Challenge = ""
	return nil
}

func (g *mcpGrant) expired() bool { return !g.Expires.IsZero() && time.Until(g.Expires) <= 0 }

func (a *Agents) mcpOAuthEntry(ctx context.Context, agentID, scope, name string) (serverEntry, error) {
	entries, err := a.config.MCPServers.list(ctx, agentID)
	if err != nil {
		return serverEntry{}, err
	}
	for _, entry := range entries {
		if entry.info.Name == name && entry.info.Scope == scope && entry.config.OAuth != nil && !entry.config.Disabled {
			return entry, validateMCPOAuth(entry.config)
		}
	}
	return serverEntry{}, ErrItemNotFound
}

func (a *Agents) StartMCPAuthorization(ctx context.Context, agentID, scope, name, redirect, state string) (string, *MCPAuthorization, error) {
	entry, err := a.mcpOAuthEntry(ctx, agentID, scope, name)
	if err != nil {
		return "", nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	fail := func(message string, cause error) (string, *MCPAuthorization, error) {
		return "", nil, &privateError{message: message, cause: errors.Join(ErrInvalidInput, cause)}
	}
	endpoint, err := mcpOAuthURL(entry.config.URL)
	if err != nil {
		return fail("invalid MCP resource URL", err)
	}
	fetch := mcpOAuthFetch{resource: endpoint}
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + mcp.LatestProtocolVersion + `","capabilities":{},"clientInfo":{"name":"dex","version":"1"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(body))
	if err != nil {
		return fail("cannot build MCP OAuth discovery request", err)
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	resp, err := fetch.Do(req) //nolint:bodyclose // Closed before discovery.
	if err != nil {
		return fail("cannot contact the MCP server", err)
	}
	_ = resp.Body.Close()
	challenge := mcpOAuthChallenge(resp)
	metadataURL := ""
	if challenge.ResourceMetadataURL != nil {
		metadataURL = challenge.ResourceMetadataURL.Href()
	}
	resource, err := oauth.DiscoverProtectedResourceMetadata(ctx, endpoint.String(), oauth.DiscoveryOptions{ResourceMetadataURL: metadataURL, Fetch: fetch})
	if err != nil || len(resource.AuthorizationServers) == 0 {
		return fail("cannot discover MCP protected resource metadata", err)
	}
	if _, err := mcpOAuthURL(resource.Resource); err != nil {
		return fail("invalid MCP resource identifier", err)
	}
	resourceID, err := oauth.SelectResource(endpoint.String(), resource)
	if err != nil {
		return fail("OAuth metadata names a different MCP resource", err)
	}
	issuer := resource.AuthorizationServers[0]
	issuerURL, err := mcpOAuthURL(issuer)
	if err != nil || issuerURL.RawQuery != "" || issuerURL.ForceQuery {
		return fail("invalid MCP authorization server issuer", err)
	}
	metadata, err := oauth.DiscoverAuthorizationServerMetadata(ctx, issuer, oauth.DiscoveryOptions{Fetch: fetch})
	if err != nil || metadata == nil {
		return fail("cannot discover MCP authorization server metadata", err)
	}
	if !slices.Contains(metadata.CodeChallengeMethodsSupported, "S256") {
		return fail("the authorization server does not advertise PKCE S256", nil)
	}
	for _, raw := range []string{metadata.AuthorizationEndpoint, metadata.TokenEndpoint} {
		u, err := mcpOAuthURL(raw)
		if err != nil || (u.Scheme == "http" && !mcpSameOrigin(u, endpoint)) {
			return fail("invalid MCP authorization server endpoint", err)
		}
	}
	old, err := entry.loadGrant(a.vault)
	if err != nil {
		return fail("cannot load the encrypted MCP grant", err)
	}
	cfg := entry.config.OAuth
	requestedScope := challenge.Scope
	if old != nil && old.Challenge != "" {
		requestedScope = oauth.StepUpScope(old.Scope, old.Challenge)
	}
	if requestedScope == "" {
		requestedScope = cfg.Scope
	}
	if requestedScope == "" {
		requestedScope = strings.Join(resource.ScopesSupported, " ")
	}
	if method := cfg.TokenEndpointAuthMethod; method != "" && len(metadata.TokenEndpointAuthMethodsSupported) != 0 && !slices.Contains(metadata.TokenEndpointAuthMethodsSupported, method) {
		return fail("the authorization server does not advertise the configured token endpoint auth method", nil)
	}
	client := oauth.OAuthClientInformation{ClientID: cfg.ClientID, TokenEndpointAuthMethod: cfg.TokenEndpointAuthMethod}
	if cfg.ClientSecret != "" {
		if _, err := entry.clientSecret(a.vault); err != nil {
			return fail("add the OAuth client secret to the vault at the connection scope", err)
		}
	}
	if client.ClientID == "" {
		switch {
		case old != nil && old.Metadata.Issuer == metadata.Issuer && old.Metadata.TokenEndpoint == metadata.TokenEndpoint && old.Redirect == redirect && old.Resource == resourceID:
			client = old.Client
		case metadata.RegistrationEndpoint == "":
			return fail("configure a pre-registered OAuth client ID; the provider does not advertise dynamic registration", nil)
		default:
			registered, err := oauth.RegisterClient(ctx, issuer, oauth.RegisterClientOptions{Metadata: metadata, Fetch: fetch, Scope: requestedScope, ClientMetadata: oauth.OAuthClientMetadata{ClientName: "Dex", RedirectURIs: []string{redirect}, ResponseTypes: []string{"code"}, GrantTypes: []string{"authorization_code", "refresh_token"}, TokenEndpointAuthMethod: "none"}})
			if err != nil {
				return fail("MCP OAuth client registration failed", err)
			}
			client = *registered
		}
	}
	link, verifier, err := oauth.StartAuthorization(issuer, oauth.StartAuthorizationOptions{Metadata: metadata, ClientInformation: client, RedirectURL: redirect, Scope: requestedScope, State: state, Resource: resourceID})
	if err != nil {
		return fail("cannot start MCP authorization", err)
	}
	return link.Href(), &MCPAuthorization{agentID: agentID, entry: entry, redirect: redirect, verifier: verifier, grant: mcpGrant{Binding: entry.grantBinding(), Resource: resourceID, Redirect: redirect, Metadata: *metadata, Client: client, Scope: requestedScope}}, nil
}

func (a *Agents) FinishMCPAuthorization(ctx context.Context, p *MCPAuthorization, code string, iss *string) error {
	g := &p.grant
	if code == "" || (iss != nil && *iss != g.Metadata.Issuer) || (iss == nil && g.Metadata.AuthorizationResponseIssParameterSupported != nil && *g.Metadata.AuthorizationResponseIssParameterSupported) {
		return errors.New("invalid MCP authorization response")
	}
	// Re-read the effective entry before exchange and again before commit. A
	// removed, changed, or shadowed declaration must not receive this grant.
	current := func() error {
		entry, err := a.mcpOAuthEntry(ctx, p.agentID, p.entry.info.Scope, p.entry.info.Name)
		if err != nil || entry.grantBinding() != g.Binding {
			return errors.New("the MCP declaration changed; start authorization again")
		}
		return nil
	}
	if err := current(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	options, err := g.tokenOptions(p.entry, a.vault)
	if err != nil {
		return err
	}
	tokens, err := oauth.ExchangeAuthorizationCode(ctx, g.Metadata.Issuer, oauth.ExchangeAuthorizationCodeOptions{TokenRequestOptions: options, Code: code, CodeVerifier: p.verifier, RedirectURL: p.redirect})
	if err != nil {
		return &privateError{message: "MCP OAuth code exchange failed; start authorization again", cause: err}
	}
	if err := g.saveTokens(tokens); err != nil {
		return err
	}
	store := a.config.MCPServers
	key := p.entry.grantKey()
	lock := &store.oauthLocks[key[0]%64]
	lock.Lock()
	defer lock.Unlock()
	store.mu.Lock()
	if err := current(); err != nil {
		store.mu.Unlock()
		return err
	}
	err = p.entry.saveGrant(a.vault, g)
	if err == nil {
		store.revisions[p.entry.scopeID]++
	}
	store.mu.Unlock()
	if err != nil {
		return err
	}
	a.markExtensionChange(p.agentID, p.entry.info.Scope, "MCP authorization")
	return nil
}

func (e serverEntry) clientSecret(v *vault.Store) (string, error) {
	return v.Exact(e.scopeID, strings.TrimPrefix(e.config.OAuth.ClientSecret, "vault:"))
}

func (g *mcpGrant) tokenOptions(entry serverEntry, v *vault.Store) (oauth.TokenRequestOptions, error) {
	u, err := mcpOAuthURL(entry.config.URL)
	if err != nil {
		return oauth.TokenRequestOptions{}, err
	}
	client := g.Client
	if entry.config.OAuth.ClientSecret != "" {
		// A pre-registered secret remains in its original vault entry.
		client.ClientSecret, err = entry.clientSecret(v)
		if err != nil {
			return oauth.TokenRequestOptions{}, err
		}
	}
	return oauth.TokenRequestOptions{Metadata: &g.Metadata, ClientInformation: client, Resource: g.Resource, Fetch: mcpOAuthFetch{resource: u}}, nil
}

func mcpOAuthChallenge(resp *http.Response) oauth.OAuthChallenge {
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return oauth.OAuthChallenge{}
	}
	for _, header := range resp.Header.Values("WWW-Authenticate") {
		for _, field := range mcpAuthChallenges(header) {
			if challenge := oauth.ParseWWWAuthenticate(field); challenge.ResourceMetadataURL != nil || challenge.Scope != "" || challenge.Error != "" {
				return challenge
			}
		}
	}
	return oauth.OAuthChallenge{}
}
