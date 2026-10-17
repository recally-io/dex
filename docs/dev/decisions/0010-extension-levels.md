# 0010: Three extension levels and agent environments

Status: Accepted (2026-10-17). Amends [ADR 0008](0008-agent-extensions.md).

## Context

Users need shared tools for dex and private tool credentials for each agent.
The data directory already defines dex home. A separate setting is not needed.

## Decision

Each level contains `skills/`, `mcp.json`, and `.env` under `.agents/`:

- Home: `$HOME/.agents/`.
- Dex: `$DEX_HOME/.agents/`, where DEX_HOME is the resolved data directory.
- Agent: `$AGENT_HOME/.agents/`, where AGENT_HOME is `$DEX_HOME/agents/<id>`.

Resolve scan roots before reading `.env`. Read equal real directories once,
as the narrower level. Never change the mode of HOME. Tools keep
`$AGENT_HOME/workspace` as their default working directory. The memory repo
stays at `$DEX_HOME/memory`. Project scope and Windows are out of scope.

A narrower skill or MCP server replaces the wider entry as a whole.
`{ "disabled": true }` disables a wider server. Any invalid `mcp.json`
prevents all MCP servers from starting for that agent.

Each agent gets a separate snapshot: worker environment, then home, dex,
and agent `.env`, merged per key. The worker never changes `process.env`.
The snapshot goes to tool processes and stdio MCP servers. A server's own
`env` wins over the snapshot. Files accept `KEY=VALUE`, blank lines, `#`
comment lines, optional `export `, and single or double quotes. There is
no interpolation, escape processing, or multiline value. Invalid lines or
keys are configuration errors. Any invalid `.env` removes all `.env` values
and prevents MCP startup for that agent. The worker environment remains.

Only enabled HTTP servers substitute `${KEY}` in headers, once, from the
snapshot. Missing keys fail that server; other servers can start. There
are no defaults, recursive substitutions, or substitutions in URLs, commands,
or arguments.

This changes ADR 0008's credential isolation claim: a home `.env` token can
reach an agent server. Disabling a server does not withdraw environment keys.
Keep credential names specific to their use.

Before loading extensions or resuming work, the worker moves old agent
`skills/` and `mcp.json` into `.agents/` when the target is absent. Each rename
can complete independently. Restart repeats the remaining moves. Existing
targets win and produce warnings. Relative links in an old skills tree that
would change meaning prevent the move and produce warnings. Symlinks move
as links. Warnings do not prevent reload.

Reload keeps the maintenance lease and restarts the whole worker. Preflight
checks every open agent and the requested agent. Any invalid configuration
refuses reload with the agent name and error. Old MCP connections close on
shutdown. The new worker loads tools before it resumes work.

The API lists current skill declarations, current server declarations,
loaded servers, warnings, and `.env` key names with level and shadowed state.
A shadowed key has a declaration at a narrower level. Edit MCP or `.env`
files, then reload. Skills are read before each model request.

## Consequences

The loader, extensions API, errors, and logs do not expose `.env` values or
substituted headers. This is not a model security boundary: the model can
read the files or run `env`, and tool output enters the transcript. The
Extensions tab states this limit. Use mode `0600` for credential files and
never put them in Git. Files and tool processes remain outside a sandbox.
