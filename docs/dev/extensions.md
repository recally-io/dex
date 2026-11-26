# Skills and Integrations

This document describes how dex loads skills, MCP servers, vault secrets, and
`AGENTS.md` for an agent, and how it reloads them.

Skills come from two file levels: dex (`<data dir>/.agents/`) and agent
(`<data dir>/agents/<id>/.agents/`). dex does not read the user home for skills.
Narrower levels win. Equal real paths are read once as the narrower level.
The engine reads `AGENTS.md` only from the conversation's cwd in its execution
environment and adds it after the bot's saved instructions at each admission.

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
`$DEX_SKILL_CACHE/<name>` with `mkdir -p` and put runtime files there. Keep
secrets in the vault. Pass references in the `env` argument of `bash` or
`process_start`.

Each host store is a Git repository. dex commits changes at submission settlement
as `Update skills from <source>`, with the same source as memory. On open, dex
recovers changes left by a crash. Git honors a skill's `.gitignore`; sync does not.

## Tool environment and MCP

Each agent has its own environment snapshot. Local tools and stdio MCP
start from the control host's login shell environment and configured
1Password credentials. Tools on a remote target use that target's environment
and the configured 1Password credentials. A command or MCP declaration can
add explicit environment variables. dex does not modify its process environment.
Skill and MCP tool writes, vault writes, and 1Password settings changes
request a reload. Runner settings changes request a reload for agents
that use the environment. A machine reconnect requests a reload if the default
machine was offline at cold open or the last reload could not open it. Each open agent reloads
when it is idle. If validation fails, dex keeps the current resources and
does not try the same configuration again automatically.

Manual skill file and shell rc edits do not request a reload. They apply at
the next reload from another change, after an explicit reload, or when dex
restarts. dex does not poll for changes or watch these files.

The [engine extension loader](../../internal/engine/extensions.go) connects
servers before it resumes the agent Harness. This is a shared wait for all
Sessions of a Bot, including Sessions that use another environment. MCP servers
use control-host configuration and do not need a lease on the default environment.
The loader installs prompt sections for the extension guide and current skills.
MCP tools use the names
`mcp__<server>__<tool>`. In owner sessions, `tool_search` searches the built-in
and MCP tools available to the current model. It adds only matching deferred
tools to the next model call. It does not expand the session's permissions.

### Secret vault

Use Settings → Secrets to set shared values. Use a bot's Secrets page to
set values for that bot. An agent entry overrides a shared entry with the same
name. `GET /api/agents/{agentId}/vault` returns names, scopes, and last change
times. `PUT` sets a value and `DELETE` removes it. Neither returns a value.
The agent has only `secret_list`, which shows effective names and metadata.

`bash`, `process_start`, and `monitor_set` accept `vault:NAME`
as an `env` value. A schedule stores the reference and resolves it when the
check runs. dex resolves values on the control host and sends only the
requested variables to the command. Local, SSH, runner, and E2B use the same
path. Remote targets need no extra program for vault access. A missing name stops the command
before it starts. A plain Bash call gets no vault values.

A snapshot masks exact values of at least 8 characters as `[vault:NAME]` in final
top-level tool results and Process wake-ups. Values passed between
nested Code Mode tools are not masked. A long Bash call's full output file
and a Process's output file stay as written on the execution host. Masking
does not hide encoded values, or block another process of the same user from
reading a process environment. The vault is not a sandbox on the control host.

The vault uses an age file for each level in `<data dir>/vault/`. Writes are
serialized and replace each file atomically. dex creates `identity.txt` with
mode `0600` on the first write. A copy without that identity cannot decrypt
the files. A process that can read the identity and the encrypted files can
read the vault. Keep the identity separate from any copy that must not grant
access to secrets.

`credentials.age` uses the same identity. It holds control-service credentials,
including the Telegram manager and managed bot tokens. Tool resolution and
vault API routes do not read this file.

Store secrets in the vault. Put other values in shell rc files or in the
`env` of a call or MCP server.

### MCP declarations

`control.sqlite` stores declarations at the dex and agent levels. The agent
level replaces the dex declaration with the same name. Use `disabled: true`
at the agent level to turn off a dex server for that agent. The database backup
includes these declarations.

All owner sessions can use `mcp_server_list`, `mcp_server_get`,
`mcp_server_set`, and `mcp_server_remove` from any execution environment.
This includes tasks, dreams, and child sessions.
`mcp_server_set` replaces one declaration. A stdio server runs on the control
host. Relative `cwd` values use the agent's control-host workspace.
`mcp_server_remove` deletes one level's declaration and can uncover a wider
declaration. Writes apply after
the current work ends and the affected agent reloads. A dex change marks all
open agents for reload.

`mcp_server_get` hides plain `env` and `headers` values and the URL query.
dex resolves `vault:NAME` and inline `${vault:NAME}` in `env` and `headers`
when it starts the server. For example, an HTTP header can use
`Authorization: Bearer ${vault:NOTION_TOKEN}`. The declarations keep references,
not resolved values. `mcp_server_get`, `mcp_server_list`, and the Integrations
API show those references as written. To keep a hidden `env` or `headers`
value, include `[hidden]` for that key in `mcp_server_set`. Do not paste
literal secrets into a declaration. A snippet can enter the transcript.
Plain values in declarations are stored as plain text.

`GET /api/agents/{agentId}/integrations` reads file metadata and MCP status.
For an open Harness, the request uses the default Runner's lease to inspect
workspace skills. If the lease has not opened, this request can open it without
blocking admission on other Runners. Bot Settings → Integrations shows
loaded MCP server state and the current OAuth grant state. Reload is available
from both Integrations and Skills.
Before the agent opens, the API shows connection state `idle` for declarations
that are not disabled.

### MCP browser authorization

For an HTTP declaration, set `oauth: {}` to use browser authorization instead
of static headers. Use `oauth.clientId` for a pre-registered client. If the
client needs a secret, set `oauth.clientSecret` to `vault:NAME`. Put the secret
in the vault at the same level as the declaration. A bot secret cannot change
the client of a dex-level connection. `oauth.tokenEndpointAuthMethod` can
select `client_secret_basic` or `client_secret_post` for a confidential client.
If the provider does not advertise a registration endpoint, dex requires a
pre-registered client. Dex does not serve Client ID Metadata Documents.

Register `<dex origin>/mcp/oauth/callback` with the provider. Open dex over
HTTPS or on loopback HTTP. Select **Authorize** or **Reauthorize** on the
connection in the bot's Integrations page. Dex probes the MCP endpoint without credentials. Dex
uses the protected resource metadata and then discovers the issuer with
RFC 8414 or OIDC. Dex uses a challenged scope when present. Otherwise dex
uses `oauth.scope` or the scopes in the protected resource metadata, in that
order. Dex uses the first listed issuer. Discovered endpoints on other origins
must use public HTTPS. Discovery and token requests do not follow redirects.

The authenticated start binds a 10-minute, one-time state to the initiating
browser, effective declaration, and MCP resource. Dex holds the PKCE verifier
in memory. The callback consumes state before exchange. Dex does not use the
regular session cookie on the cross-site return. It uses a separate Lax,
HttpOnly cookie that the authenticated start set for this attempt. Dex sends
the resource indicator during authorization, exchange, and refresh.

The one dex owner has one grant per declaration level and name. A dex-level
grant is shared across bots that use that declaration. A same-name bot
connection uses a separate grant. Dex encrypts grants in private `vault/mcp-*.age`
files with the vault identity. Vault listing and reference resolution do not
read these files. A declaration replacement that changes the OAuth settings
or URL cannot use the old grant. The server reads the current pre-registered
secret from its vault entry at exchange and refresh. To preserve grants,
back up the private vault files with the other vault files.

A successful callback opens `/a/{agentId}/integrations` and marks the affected connections for idle reload. Dex refreshes an expired token when a refresh token is available.
On HTTP 401, dex attempts at most one refresh and retry. If the grant no longer
works, Integrations shows **Reauthorize**. Dex does not fall back to static headers.

### Reload

`POST /api/agents/{agentId}/integrations/reload`
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
