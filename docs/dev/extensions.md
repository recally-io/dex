# Extensions

This document describes how dex loads skills, MCP servers, `.env`, and
`AGENTS.md` for an agent, and how it reloads them.

Skills and `.env` come from two file levels: dex
(`<resolved data dir>/.agents/`) and agent (`<data dir>/agents/<id>/.agents/`).
dex does not read the user home. Narrower levels win. Paths are fixed before
`.env` is read. Equal real paths are read once as the narrower level.
The engine reads `AGENTS.md` only from the conversation's cwd in its execution
environment and adds it after the bot's saved instructions at each admission.
On upgrade, dex imports old dex-level and agent-level files, including their
imports, into the affected bots' saved instructions. It renames the old files
with the `.migrated` suffix. Startup skips a source if its backup exists. If a
rename fails, the next start can import the same source again. Without a backup,
startup treats any file at a legacy path as a source to import. The engine
never loads files from those levels directly.

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

## Skills

A directory with `SKILL.md` is one skill. Discovery stops there and skips
hidden directories and `node_modules`. The front matter `name` wins over the
directory name. A second skill with the same name in one level is a warning
and does not block MCP Reload.

The dex and agent stores are on the control host. All owner sessions, including
tasks, dreams, and child sessions, use `skill_list` and `skill_read` to inspect
these stores. Use `skill_write`, `skill_edit`, and `skill_delete` to change them.
Use a path such as `agent/deploy/run.sh`. Set `mode` to the octal string `0755`
for an executable script. To delete a whole skill, pass only its level and
directory name to `skill_delete`. Normal `write` and `edit` reject paths in the
stores or their remote copies. Bash has no such path check. Project skills
still use the normal file tools.

Run installers such as `npx skills add` in a temporary directory in the session
environment. Call `skill_import` with the installed skill directory, the `level`,
and an optional new directory `name`. The import goes to the control host only.
Import and sync copy all files in a skill, including dotfiles and `node_modules`.
They skip nested `.git` entries. They copy a symlink as its target only if the
target is inside the skill. `skill_list` reports skipped external links.

A local session uses the stores directly. Each remote lease gets a read-only
copy at `<workspace>/.dex/skills/<scope>/`. A `.manifest.json` records the paths,
SHA-256 hashes, and host modes. A sync writes changed files with their host mode
minus write bits. It removes missing managed files and leaves other files in
place. It removes the whole directory when the host removes a skill. It reuses
a host file's hash until the size or modification time changes.

Sync runs when a lease opens or a runner reconnects, after a skill mutation in
the current session, and on prompt render when the host stamp changed. A mutation
returns the path in the current session environment after sync. A later tool
call in the same Code Mode script can use this path without another prompt
render. If sync fails, dex logs the failure and keeps unrelated tools available.
A skill mutation reports a sync failure to its caller.

Run scripts from the skill directory in the session environment. Every `bash`
call sets `DEX_SKILL_CACHE` to `<workspace>/.dex/cache/skills`. Create
`$DEX_SKILL_CACHE/<name>` with `mkdir -p` and put runtime files there. A skill's
`.env` is an ordinary file. dex copies it and does not load it. Keep secrets
in the vault. Pass them with the `env` argument of `bash` or
`monitor_set_command`.

Each host store is a Git repository. dex commits changes at submission settlement
as `Update skills from <source>`, with the same source as memory. On open, dex
recovers changes left by a crash. Git honors a skill's `.gitignore`; sync does not.

## Tool environment and MCP

Each agent has its own environment snapshot for tools and stdio MCP servers.
It starts from the login shell environment, then the `.env` levels, then the
1Password credentials of the settings. When `op` exists, the prompt tells the
agent to read secrets with `op read`. Optional speech can resolve an explicitly
selected provider key reference in memory through the same CLI and credentials.
Other references remain under agent control. A scheduler job reloads an open
agent when that agent is idle after an extension, shell rc, or 1Password
configuration change.
Invalid `.env` files prevent MCP startup for that agent. Invalid
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

### MCP declarations

`control.sqlite` stores declarations at the dex and agent levels. The agent
level replaces the dex declaration with the same name. Use `disabled: true`
at the agent level to turn off a dex server for that agent. The database backup
includes these declarations. dex ignores `mcp.json` at both levels and logs one
warning for each file at startup. It does not import the files.

All owner sessions can use `mcp_server_list`, `mcp_server_get`,
`mcp_server_set`, and `mcp_server_remove` from any execution environment.
This includes tasks, dreams, and child sessions. Isolated Telegram group
sessions keep their existing tool allowlist and cannot use these tools.
`mcp_server_set` replaces one declaration. A stdio server runs on the control
host. Relative `cwd` values use the agent's control-host workspace.
`mcp_server_remove` deletes one level's declaration and can uncover a wider
declaration. Writes apply after
the current work ends and the affected agent reloads. A dex change marks all
open agents for reload.

`mcp_server_get` hides plain `env` and `headers` values and the URL query.
Vault references are visible as written. The vault resolves them at server
startup once vault support is available. To keep a hidden `env` or `headers`
value, include `[hidden]` for that key in `mcp_server_set`. Ask the user to
move secrets to the vault before supplying a JSON snippet. Snippets can enter
the transcript. Plain values in declarations are stored as plain text.

`GET /api/agents/{agentId}/extensions` reads file metadata and MCP status.
The Web Extensions page uses the same server status as `mcp_server_list`.
Before the agent opens, the API shows declarations that are not disabled as
`idle`.

### Reload

`POST /api/agents/{agentId}/extensions/reload`
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
