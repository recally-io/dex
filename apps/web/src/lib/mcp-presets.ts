import type { McpDraft } from "./mcp-declaration";

type Preset = McpDraft & { title: string };

const driveScopes = ["drive.readonly", "drive.file"];

function googlePreset(title: string, product: string, scopes: string[], productApi?: string): Preset {
  return {
    title,
    name: product === "gmail" ? "gmail" : `google-${product}`,
    url: `https://${product}mcp.googleapis.com/mcp/v1`,
    auth: "oauth-client",
    scope: scopes.map((scope) => `https://www.googleapis.com/auth/${scope}`).join(" "),
    productApi,
  };
}

export const mcpPresets: { group: string; servers: Preset[] }[] = [
  {
    group: "Work",
    servers: [
      { title: "Notion", name: "notion", url: "https://mcp.notion.com/mcp", auth: "oauth" },
      { title: "Linear", name: "linear", url: "https://mcp.linear.app/mcp", auth: "oauth" },
      { title: "Atlassian", name: "atlassian", url: "https://mcp.atlassian.com/v2/mcp", auth: "oauth" },
      { title: "Figma", name: "figma", url: "https://mcp.figma.com/mcp", auth: "oauth" },
      { title: "Stripe", name: "stripe", url: "https://mcp.stripe.com", auth: "oauth" },
      { title: "PayPal", name: "paypal", url: "https://mcp.paypal.com/mcp", auth: "oauth" },
    ],
  },
  {
    group: "Development",
    servers: [
      {
        title: "GitHub",
        name: "github",
        url: "https://api.githubcopilot.com/mcp/",
        auth: "header",
        headers: [
          {
            name: "Authorization",
            value: "",
            prefix: "Bearer ",
            secret: true,
            required: true,
            description: "Personal access token",
          },
        ],
      },
      { title: "Sentry", name: "sentry", url: "https://mcp.sentry.dev/mcp", auth: "oauth" },
      { title: "Vercel", name: "vercel", url: "https://mcp.vercel.com", auth: "oauth" },
      { title: "Supabase", name: "supabase", url: "https://mcp.supabase.com/mcp", auth: "oauth" },
      { title: "Hugging Face", name: "hugging-face", url: "https://huggingface.co/mcp", auth: "none" },
    ],
  },
  {
    group: "Google Workspace",
    servers: [
      googlePreset(
        "Gmail",
        "gmail",
        ["gmail.readonly", "gmail.compose", "gmail.modify"],
        "gmail.googleapis.com",
      ),
      googlePreset("Google Drive", "drive", driveScopes, "drive.googleapis.com"),
      googlePreset(
        "Google Docs",
        "docs",
        [...driveScopes, "documents.readonly", "documents"],
        "docs.googleapis.com",
      ),
      googlePreset(
        "Google Sheets",
        "sheets",
        [...driveScopes, "spreadsheets.readonly", "spreadsheets"],
        "sheets.googleapis.com",
      ),
      googlePreset(
        "Google Slides",
        "slides",
        [...driveScopes, "presentations.readonly", "presentations"],
        "slides.googleapis.com",
      ),
      googlePreset(
        "Google Calendar",
        "calendar",
        [
          "calendar.calendarlist.readonly",
          "calendar.events.freebusy",
          "calendar.events.readonly",
          "calendar.events",
        ],
        "calendar-json.googleapis.com",
      ),
      googlePreset(
        "Google Chat",
        "chat",
        [
          "chat.spaces.readonly",
          "chat.memberships.readonly",
          "chat.messages.readonly",
          "chat.messages.create",
          "chat.users.readstate",
          "chat.users.spacesettings",
        ],
        "chat.googleapis.com",
      ),
      googlePreset("Google Tasks", "tasks", ["tasks.readonly", "tasks"]),
    ],
  },
  {
    group: "Documentation",
    servers: [
      { title: "Context7", name: "context7", url: "https://mcp.context7.com/mcp", auth: "none" },
      {
        title: "Cloudflare Docs",
        name: "cloudflare-docs",
        url: "https://docs.mcp.cloudflare.com/mcp",
        auth: "none",
      },
      { title: "DeepWiki", name: "deepwiki", url: "https://mcp.deepwiki.com/mcp", auth: "none" },
    ],
  },
];
