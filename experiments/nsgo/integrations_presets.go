package main

import "strings"

type integrationsPreset struct {
	title string
	draft integrationsMCPDraft
}

type integrationsPresetGroup struct {
	name    string
	entries []integrationsPreset
}

func integrationsPresetFor(title, name, address, auth string) integrationsPreset {
	return integrationsPreset{title, integrationsMCPDraft{Name: name, URL: address, Auth: auth}}
}

func integrationsGoogle(title, product string, scopes ...string) integrationsPreset {
	name := product
	if name != "gmail" {
		name = "google-" + name
	}
	p := integrationsPresetFor(title, name, "https://"+product+"mcp.googleapis.com/mcp/v1", "oauth-client")
	for i, s := range scopes {
		scopes[i] = "https://www.googleapis.com/auth/" + s
	}
	p.draft.Scope = strings.Join(scopes, " ")
	return p
}

// Match the official choices in web/src/lib/mcp-presets.ts. Adding a preset only fills the form.
func integrationsPresets() []integrationsPresetGroup {
	preset := integrationsPresetFor
	github := preset("GitHub", "github", "https://api.githubcopilot.com/mcp/", "header")
	github.draft.Headers = []integrationsHeader{{Name: "Authorization", Description: "Personal access token in the vault", Required: true, Secret: true}}
	return []integrationsPresetGroup{
		{"Work", []integrationsPreset{
			preset("Notion", "notion", "https://mcp.notion.com/mcp", "oauth"),
			preset("Linear", "linear", "https://mcp.linear.app/mcp", "oauth"),
			preset("Atlassian", "atlassian", "https://mcp.atlassian.com/v2/mcp", "oauth"),
			preset("Figma", "figma", "https://mcp.figma.com/mcp", "oauth"),
			preset("Stripe", "stripe", "https://mcp.stripe.com/mcp", "oauth"),
			preset("PayPal", "paypal", "https://mcp.paypal.com/mcp", "oauth"),
		}},
		{"Development", []integrationsPreset{
			github,
			preset("Sentry", "sentry", "https://mcp.sentry.dev/mcp", "oauth"),
			preset("Vercel", "vercel", "https://mcp.vercel.com/mcp", "oauth"),
			preset("Supabase", "supabase", "https://mcp.supabase.com/mcp", "oauth"),
			preset("Hugging Face", "hugging-face", "https://huggingface.co/mcp", "none"),
		}},
		{"Google Workspace", []integrationsPreset{
			integrationsGoogle("Gmail", "gmail", "gmail.readonly", "gmail.compose", "gmail.modify"),
			integrationsGoogle("Google Drive", "drive", "drive.readonly", "drive.file"),
			integrationsGoogle("Google Docs", "docs", "drive.readonly", "drive.file", "documents.readonly", "documents"),
			integrationsGoogle("Google Sheets", "sheets", "drive.readonly", "drive.file", "spreadsheets.readonly", "spreadsheets"),
			integrationsGoogle("Google Slides", "slides", "drive.readonly", "drive.file", "presentations.readonly", "presentations"),
			integrationsGoogle("Google Calendar", "calendar", "calendar.calendarlist.readonly", "calendar.events.freebusy", "calendar.events.readonly", "calendar.events"),
			integrationsGoogle("Google Chat", "chat", "chat.spaces.readonly", "chat.memberships.readonly", "chat.messages.readonly", "chat.messages.create", "chat.users.readstate", "chat.users.spacesettings"),
			integrationsGoogle("Google Tasks", "tasks", "tasks.readonly", "tasks"),
		}},
		{"Documentation", []integrationsPreset{
			preset("Context7", "context7", "https://mcp.context7.com/mcp", "none"),
			preset("Cloudflare Docs", "cloudflare-docs", "https://docs.mcp.cloudflare.com/mcp", "none"),
			preset("DeepWiki", "deepwiki", "https://deepwiki.com/mcp", "none"),
		}},
	}
}

func integrationsPresetAuth(auth string) string {
	switch auth {
	case "oauth":
		return "Browser OAuth"
	case "oauth-client":
		return "Registered OAuth client"
	case "header":
		return "Token"
	default:
		return "No sign-in"
	}
}
