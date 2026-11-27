package engine

import (
	"net/url"
	"testing"

	"github.com/MichaelKinsy/PiG/mcp/oauth"
)

func TestUserRequestsOfflineGoogleMCPAccess(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		issuer  string
		offline bool
	}{
		{name: "Google", issuer: "https://accounts.google.com", offline: true},
		{name: "other issuer", issuer: "https://accounts.google.com.example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Given a discovered issuer and a pre-registered client with a product scope.
			const scope = "https://www.googleapis.com/auth/gmail.readonly"
			// When Dex builds the authorization URL, the Google parameters apply only to Google.
			link, verifier, err := startMCPAuthorizationURL(tc.issuer+"/", oauth.StartAuthorizationOptions{
				Metadata: &oauth.AuthorizationServerMetadata{
					Issuer: tc.issuer, AuthorizationEndpoint: tc.issuer + "/authorize?existing=keep",
					ResponseTypesSupported: []string{"code"}, CodeChallengeMethodsSupported: []string{"S256"},
				},
				ClientInformation: oauth.OAuthClientInformation{ClientID: "fixture-client"},
				RedirectURL:       "http://localhost:8787/mcp/oauth/callback", Scope: scope,
				State: "fixture-state", Resource: "https://mcp.example.test/mcp",
			})
			if err != nil {
				t.Fatal(err)
			}
			q, err := url.ParseQuery(link.Query)
			if err != nil {
				t.Fatal(err)
			}
			if tc.offline {
				if q.Get("access_type") != "offline" || q.Get("prompt") != "consent" {
					t.Fatalf("Google authorization parameters: access_type=%q, prompt=%q", q.Get("access_type"), q.Get("prompt"))
				}
			} else if q.Has("access_type") || q.Has("prompt") {
				t.Fatal("Google parameters reached a different issuer")
			}
			if q.Get("scope") != scope || q.Get("existing") != "keep" || q.Get("resource") != "https://mcp.example.test/mcp" || q.Get("state") != "fixture-state" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || verifier == "" {
				t.Fatal("authorization lost the configured scope, resource, state, or PKCE")
			}
		})
	}
}
