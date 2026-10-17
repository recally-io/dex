# 0008: Skills and MCP servers from files

Status: Amended by [ADR 0010](0010-extension-levels.md) (2026-10-17)

## Context

The agent has four tools and the memory. The user wants to extend an agent
with skills and with tools from MCP servers. Other agent tools keep these in
files: skills in `~/.agents/skills/<name>/SKILL.md` (the Agent Skills format),
and MCP servers in `mcp.json` files with an `mcpServers` object. The user
wants the same files for dex, globally and for each agent.

[ADR 0002](0002-settings-in-control-db.md) says that `control.sqlite` is the
only source of settings. Extension files are an exception to it.

## Decision

- Two scopes. Global: `~/.agents/skills/` and `~/.agents/mcp.json`. Agent:
  `<data dir>/agents/<id>/skills/` and `<data dir>/agents/<id>/mcp.json`. An
  agent entry replaces a global entry with the same name as a whole, so a
  scope never gets the credentials of the other. An agent entry
  `{ "disabled": true }` turns off a global server.
- The files are the configuration. dex keeps no copy in `control.sqlite`,
  and the web UI only shows them. Models, providers, Telegram and the other
  settings stay in `control.sqlite`.
- The worker owns the extensions. It connects the servers before it resumes
  an agent, so a run that resumes after a restart has its tools.
- Skills: the prompt lists the name, description and `SKILL.md` path of each
  skill. The agent reads a skill with its read tool when a task needs it. The
  worker reads the skill files before each model request, so an edit applies
  to the next request.
- MCP: `@earendil-works/pi-mcp`, from the same project as pi-durable,
  connects over stdio and Streamable HTTP. Each tool is
  `mcp__<server>__<tool>` and is deferred: the model finds it with
  `tool_search`. A server that fails has no tools; the other servers and the
  chat still work. When an mcp.json file is not valid, no server starts,
  because its entries could replace or turn off global ones.
- MCP tools keep pi-durable's default replay policy, `unsafe`. After a
  restart, an interrupted call gets an error result; dex does not send it
  again, because the call can have changed data on the server.
- An MCP reload restarts the worker under the maintenance lease that updates
  use. The lease refuses the reload while the agent has work, and holds new
  work until the new worker runs. dex has no code to replace servers in a
  running worker. A reload is refused when a file is not valid, so the agent
  keeps its servers.

## Consequences

- An edit to mcp.json applies after a reload or a restart. A reload ends the
  open chat streams, and the browser opens them again.
- mcp.json holds headers and environment values as plain text. The API does
  not return commands, URLs, headers, environment values or raw errors, and
  the worker log has only the category of a failure.
- An MCP server runs as the user, without a sandbox. A user who can chat with
  the agent can make it write an mcp.json file, because the agent tools have
  no sandbox either.
- No OAuth, no legacy HTTP+SSE transport, and no MCP prompts or resources.
  pi-mcp has OAuth; add it when a server needs it.
- A server that drops its connection stays down until a reload.

## Amendment

[ADR 0010](0010-extension-levels.md) replaces the two scopes, agent paths,
credential isolation claim, and reload preflight in this decision.
