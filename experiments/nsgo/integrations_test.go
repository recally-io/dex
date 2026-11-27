package main

import (
	"strings"
	"testing"
)

func TestUserKeepsInvalidTargetsSeparateFromShared(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"target=", "target=%", "target=first&target=second", "target=first&target=%"} {
		q := integrationsReadQuery(raw)
		if q.missing || q.target != "" || !strings.Contains(q.change("mcp"), "target=") {
			t.Fatalf("invalid target %q became shared: %+v", raw, q)
		}
	}
	if !integrationsReadQuery("section=mcp").missing || integrationsReadQuery("target=atlas&broken=%").target != "atlas" {
		t.Fatal("the query must preserve a valid target and the absence of a target")
	}
}

func TestUserCanSaveMCPInTheSelectedScope(t *testing.T) {
	t.Parallel()
	// Given a new OAuth declaration, the same form can save to either scope.
	draft := integrationsMCPDraft{Name: "demo", URL: "http://127.0.0.1:9000/mcp", Auth: "oauth", Secret: "MCP_CLIENT", Client: "dex"}
	for bot, path := range map[string]string{"": "/api/integrations/mcp", "atlas": "/api/bots/atlas/integrations/mcp"} {
		got, body, err := integrationsMCPRequest(bot, draft)
		if err != nil || got != path || body.OAuth == nil || body.OAuth.Secret != "vault:MCP_CLIENT" {
			t.Fatalf("target %q: path %q, body %+v, error %v", bot, got, body, err)
		}
	}

	// When the URL contains credentials or a remote plaintext destination, no declaration can be sent.
	for _, address := range []string{"http://example.com/mcp", "https://user:pass@example.com/mcp", "https://example.com/mcp?key=value", "https://example.com/mcp#token"} {
		draft.URL = address
		if _, _, err := integrationsMCPRequest("", draft); err == nil {
			t.Fatalf("accepted an unsafe MCP URL: %s", address)
		}
	}
}

func TestUserCannotSaveAPlaintextAuthorizationHeader(t *testing.T) {
	t.Parallel()
	draft := integrationsMCPDraft{Name: "demo", URL: "http://localhost:9876/mcp", Auth: "header",
		Headers: []integrationsHeader{{Name: "Authorization", Value: "Bearer local-test-only"}}}
	if _, _, err := integrationsMCPRequest("", draft); err == nil {
		t.Fatal("an Authorization header must use an existing vault reference")
	}
	draft.Headers[0].Value = "Bearer ${vault:BOT_TOKEN}"
	_, body, err := integrationsMCPRequest("", draft)
	if err != nil || body.Headers["Authorization"] != "Bearer ${vault:BOT_TOKEN}" {
		t.Fatalf("valid vault reference lost: %+v, %v", body, err)
	}
}

func TestUserCanStartReviewOnlyForASupportedSkillSource(t *testing.T) {
	t.Parallel()
	skill := integrationsSkillHit{Source: "https://github.com/example/skills/tree/main/skills/test", Directory: "skills/test"}
	text, err := integrationsSkillPrompt(skill, "dex", "test")
	if err != nil || !strings.Contains(text, skill.Source) || !strings.Contains(text, `"level":"dex"`) {
		t.Fatalf("review prompt lost the chosen source or scope: %v", err)
	}
	for _, source := range []string{"http://github.com/example/skills/tree/main/test", "https://example.com/test", "https://github.com/example/skills/tree/main/../test"} {
		skill.Source = source
		if _, err := integrationsSkillPrompt(skill, "dex", "test"); err == nil {
			t.Fatalf("accepted unsupported source %q", source)
		}
	}
}
