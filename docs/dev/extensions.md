# Extensions

This document describes how dex loads skills, MCP servers, `.env`, and
`AGENTS.md` for an agent, and how it reloads them.

Skills, MCP servers, and `.env` come from two levels: dex
(`<resolved data dir>/.agents/`) and agent (`<data dir>/agents/<id>/.agents/`).
dex does not read the user home. Narrower levels win. Paths are fixed before
`.env` is read. Equal real paths are read once as the narrower level.
The engine reads `AGENTS.md` only from the conversation's cwd in its execution
environment and adds it after the bot's saved instructions at each admission.
On upgrade, dex imports old dex-level and agent-level files, including their
imports, into the affected bots' saved instructions. It renames the old files
with the `.migrated` suffix. New files at those levels have no effect.
Skills have a third, narrowest level: `.agents/skills` under the
conversation's cwd. The skills prompt section reads that cwd from the request's
execution environment, so it follows the conversation. Chat sessions store
their `cwd` in `control.sqlite`. Empty uses the workspace of the session's
execution environment. Every submit sets or clears the native cwd from that row.
Child sessions copy the parent's cwd when they are created, but only when the
child runs in the same execution environment as its parent. The `project` prompt section
states the current execution directory.

`GET /api/directories` lists visible host directories with the same path validation
as project changes. It follows directory symlinks, marks Git repositories, sorts
names without case, and returns at most 500 entries. It requires the same API
authentication and never returns file contents. `GET /api/agents/{agentId}/projects`
reads distinct conversation paths in activity order with one control query.
It checks each path and returns up to eight existing directories.
The Chat picker uses these routes because a remote browser cannot select host paths.

`PUT /api/agents/{agentId}/chats/{chatId}/project` changes the cwd for the
next message. Main, Chat, and child sessions can set it. The API and `set_project`
tool share one path validator: trim, expand `~` or `~/`, require an existing
absolute directory, and clean the path. Empty clears the override.
The foreground tool writes control state before it commits native
`harness.Configure`, so later tools in the same run use the new directory.
Replay writes the same value. Main and Chat can use the tool; automation calls
return an error. Child sessions and dreams do not have it. Channel input
uses Main's saved project. The Chat header refetches on tool
completion and run completion. For a project cwd, native input uses absolute
upload paths. The transcript keeps workspace-relative upload paths.

The Extensions API reads the agent workspace. Each level is walked like Pi's skill loader: a directory with
`SKILL.md` is one skill and the walk stops there; hidden directories and
`node_modules` are skipped; symlinked directories are read once. The front matter
`name` wins over the directory name. A second skill with the same name in one
level is a warning, not an error, so it does not block MCP Reload.
Each agent has its own environment snapshot for tools and stdio MCP servers.
It starts from the login shell environment, then the `.env` levels, then the
1Password credentials of the settings. When `op` exists, the prompt tells the
agent to read secrets with `op read`. Optional speech can resolve an explicitly
selected provider key reference in memory through the same CLI and credentials.
Other
references remain under agent control. A scheduler job
reloads an open agent after its extension files, shell rc files, or
1Password settings change, when all agents are idle.
Invalid `.env` or MCP files prevent all MCP startup for that agent. Invalid
`.env` also removes all file environment values from that snapshot.
The [engine extension loader](../../internal/engine/extensions.go) connects
servers before it resumes the agent Harness. It installs prompt sections for
the extension guide and current skills. MCP tools use the names
`mcp__<server>__<tool>` and deferred tool discovery. The guide lists fixed paths,
file formats, environment use, secrecy, and Reload. It contains no environment
key names or values, except the names of keys whose value has a shape hint
(an assignment, empty, whitespace, or an `op://` reference). It also says that
`.env` values reach only the tools that dex starts.

`PUT` and `DELETE /api/agents/{agentId}/extensions/env` call the engine's
write-only environment methods. Edits are serialized per path and replace
the file atomically with mode `0600`. They preserve unrelated lines and refuse
invalid files. Home is read-only. Saving does not reload; the automatic reload
applies the change. The file is the only store.
Each `env` entry of the Extensions API carries `hints`, the shape hints of its
value. A hint never holds value text.

`GET /api/agents/{agentId}/extensions` reads file metadata. Before the agent
opens, servers are `idle`. `POST /api/agents/{agentId}/extensions/reload`
validates the selected agent and requires idle execution of that agent only:
no open, task, submission, settlement, or tool call of the agent can be in
progress. Work of other agents does not block it. The idle check and
registration of the selected replacement are atomic. Reload does not hold the
global maintenance pause. A shutdown or a restore still waits for all agents. After preflight succeeds, other
agents can start work while the selected agent closes and opens its resources.
If the selected agent has not opened, Reload opens it and reports the MCP
connection results. A failed preflight keeps its current resources.

Each agent has its own resource lock. Concurrent calls for that agent wait with
context cancellation. The global lock protects resource state; file access,
MCP connection, and resource close run outside it. The engine tracks opens and
reloads before resource work starts. Maintenance fails busy during an active
reload, and confirmed maintenance blocks Reload. Close waits for tracked
resource work and cannot leave replacement resources open. Recovery isolation
must be registered before a Harness resumes.

The session extension adds the session tools and the `session-rules` section to
foreground conversations, including main, Chat and automation sessions.
Main input replaces the `work-loop` section with the `front-desk` section.
Chat and automation sessions keep `work-loop`.
Child sessions and dreams have no session tools and neither section. A child gets
the `child-rules` section.
The engine has no built-in coding skill. The usage of the session tools is in
the descriptions of the tools. Examples are the continue-in-context rule of
`session_send`, the concurrency of children in `session_new`, and the no-retry
rule. A `coding` skill that the user writes is an ordinary skill and is listed
with the others.
