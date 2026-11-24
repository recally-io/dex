# Architecture

This document is for developers who change dex. It is an overview: the runtime,
the data owners, the message flow, and the failure behavior. Each subsystem has
its own document, listed in [Subsystems](#subsystems).

## Runtime overview

![Runtime boundaries and data owners](../assets/architecture.png)

Source: [architecture.puml](../assets/src/architecture.puml).

dex runs HTTP, channels, business services, and agent execution in one Go
process. Business services call `internal/engine` directly. The engine uses
[PiG](https://github.com/MichaelKinsy/PiG) for durable execution, model access,
and MCP. The browser receives typed transcript events through SSE.

## Runtime modules

The Go binary embeds the built web UI. Node and Bun are development tools
for the web UI; the installed service needs neither runtime.

The [module index](../../.agents/rules/implementation.md#modules) defines
package ownership. `internal/app` connects the services. Business services call
`internal/engine`. Platform adapters implement the `internal/channel` contract.
`internal/channel/delivery` owns commands and
durable reply delivery. The engine does not import dex business packages.

Tools and stdio MCP servers can start child processes. Bash has a default
120-second timeout. An omitted or null `timeout` uses the default. An explicit
`timeout` in seconds replaces it. PiG stops the shell process group and waits for it on timeout or cancellation.
Use `monitor_set_command` for supervised long work. It has its own timeout defaults.
Other tools do not receive the bash or background timeout defaults. MCP calls have
a 600-second timeout, but remote work can continue after cancellation. A generic
deadline cannot safely stop a tool that ignores cancellation.
Agent execution itself stays in dex. There is no backend subprocess or serialized internal protocol.

## Service and updates

`cmd/dex` runs the foreground server or manages a per-user launchd or systemd
service. A host that is not loopback makes the API require the access token.
Definitions store serve
arguments, a small service PATH, and a configuration record that permits partial
reinstall across template changes. Install does not copy the shell PATH. Daily
updates add only the selected `gh` directory. Agent tools use the login shell
environment separately.

`internal/update` verifies GitHub release assets (falling back to unauthenticated
public release downloads when no `GH_TOKEN` or `gh` credentials are present),
stages beside the executable, verifies the staged binary with `version`, and
retains `dex.previous`. `internal/sysops` coordinates in-process server
operations for `/api/system/*`: persisting the release channel (`stable` or
`prerelease`) in `system_settings`, checking and staging releases with byte
progress, taking `VACUUM INTO` hot backups of `control.sqlite` in
`<dataDir>/backups/` before upgrading or on demand, capturing recent `slog`
records in an in-memory ring buffer, and waiting on the maintenance lease before
shutting down HTTP/App cleanly and re-execing the binary in place via
`syscall.Exec`. `chat.Service` owns a two-minute maintenance lease.
The engine closes admission and checks active operations, durable work, and
pending settlement and memory commits. A busy check releases the gate and
refuses maintenance. A short global receipt gate orders receipt commits and
lease confirmation. After lease confirmation, web input returns `409` before
the service writes a receipt. A web receipt committed before confirmation can
return `202` and remain pending until release. Channel input can also retain
a pending business receipt. Reconciliation resumes admission after maintenance.
The client keeps the same request ID when it retries.
A process restart clears the in-memory lease.

A remote runner reads the server release tag when it connects. The server serves
runner assets for that tag through `/machine/{id}/release/{file}` after machine
authentication. It streams assets to disk and verifies each archive before it
adds the archive to the cache. The cache keeps past tags until an operator
removes them. `dex-agent` uses the same executable verification and replacement
code as `dex`. If re-exec fails, the agent restores the previous binary.

New transcript acquisition uses the engine maintenance admission gate because
opening an agent can resume stored work. Existing subscriptions remain
independent of new admission.
`GET /api/health` exposes native readiness as `runtimeReady`.
Startup registers saved credentials and static models before it serves HTTP.
Network model discovery runs in the background. Health and Settings remain
available while discovery is pending. A cold agent cannot open during discovery
because opening resumes all its stored conversations. Its input receipts stay
pending and reconcile when discovery finishes. Direct history, reload, and
model checks that need discovery return `503`. SSE clients reconnect.
Shutdown cancels discovery and waits for its work to end. Startup failure and
shutdown use one cleanup path. Cleanup stops scheduler jobs and channel bots
before the chat consumer, then closes execution resources and business storage.

See [service commands and recovery](../../README.md#run-and-manage-dex).

## Data ownership

`control.sqlite` holds business facts: settings, agents, conversations, input
receipts, the channel outbox, Tasks, and plugin tables. Core migrations own
the core tables. Each enabled plugin applies its own `plugin_<name>_` tables at
start ([plugins](plugins.md#plugin-migrations)). Each agent has
`agents/<id>/state.sqlite`, which holds native admission, runs, transcripts,
and tool calls. Only the engine opens an agent execution store, and business
code calls typed engine methods instead of reading it.

The default data directory is `~/.dex` (`DEX_DATA_DIR`). Only the engine opens
each agent execution store. Run one dex process per data directory.

## Message flow

![Message flow](../assets/message-flow.png)

Source: [message-flow.puml](../assets/src/message-flow.puml)

1. The browser opens `GET /api/agents/{agentId}/chats/main`, then sends
   `POST /api/agents/{agentId}/chats/{id}/messages` with a client UUID.
   `chat.Service.Send` rejects ordinary input when no model is selected.
   It records the input intent and origin in `control.sqlite`.
2. The chat service creates an execution conversation on first use and stores
   its reference. It calls `Agents.Submit` with the UUID as the stable native
   request ID. PiG commits admission before it returns a handle. Only then
   does the chat service set `admitted_at` on the business receipt.
3. If admission or the receipt write fails, reconciliation retries the same
   request ID. A pending receipt alone does not prove native admission and
   must not suppress that retry. Each Session has an independent delivery gate
   and retry delay. SQLite receipt insertion order defines FIFO in that Session.
   A blocked input does not hold the delivery gate of another Session of the same Bot.
   Cold Harness initialization and recovery share the execution store and registry.
   Cold open waits for startup model discovery without holding a delivery gate of
   another Session. Sessions on the same environment can share its lease. The
   engine does not hold its lease registry lock during remote preparation.
   Receipt creation and native admission have distinct timestamps. Structured
   logs correlate Session waits, native start, and settlement without logging
   message content or remote errors. See the [slow input procedure](sessions.md#diagnose-a-slow-session-input).
4. The engine runs the model and tools. Each browser subscription receives
   typed transcript events through `live.Hub`. Queued input comes from the
   native Inbox, not from a second control transcript.
5. The chat service waits on the native handle and stores the outcome in the
   business receipt. The channel outbox reads settled receipts and records
   confirmed reply parts. A restart reacquires native outcomes by request ID;
   it does not restart a second execution loop in control.
   Telegram and Feishu/Lark status reactions observe these facts independently
   of the outbox. See [Channels](channels.md#shared-channels).

A fork request can carry a UUID. Control commits its receipt with the new Chat.
A retry with the same source and boundary returns that Chat; a mismatch returns
`409`. A concurrent retry can create an extra native conversation before control
rejects its receipt. That conversation has no control reference and is invisible.

Manual and automatic compaction use the native range selection and keep recent
context unchanged. Dex supplies a local view of older context instead of calling
a model for a summary. The view contains selected user and assistant text, with
native entry IDs. It is limited to 4 KiB, including up to 1 KiB of manual compact
instructions. It does not copy tool output, thinking, or attachments. Compaction
does not delete entries from session history or change who can read them.

Manual compact with a UUID runs at most once per ID. Control records the chat and
instructions before the native call. The native transaction commits a request
record with the compaction task. Every retry calls native admission again, so a
control receipt alone cannot suppress a task that did not commit. A synchronous
failure can be retried with the same ID. A crash after native commit does not
create a second task. The control receipt also prevents reuse across agents.

A client retry with the same UUID reuses the input intent and native admission.
The chat, input kind, text, attachments and send mode must match the first request.
An omitted mode is the same as `followUp`. A mismatch returns `409`.
The two stores do not share a transaction. Stable request IDs and receipt
reconciliation cover the gap between their commits.

## Subsystems

Each subsystem has its own document. This overview gives only the boundaries.

| Document                      | Covers                                                                     |
| ----------------------------- | -------------------------------------------------------------------------- |
| [Agents](agents.md)           | Agents and the HTTP API, settings, memory, and bot tools                   |
| [Sessions](sessions.md)       | Main, Chats, child sessions, internal delegation, and session environments |
| [Tasks](tasks.md)             | The Task Inbox, the event store, holds, Task alerts, and Task tools        |
| [Plugins](plugins.md)         | Optional Task sources: GitHub, Webhook, email, and the plugin boundary     |
| [Execution](execution.md)     | The native engine boundary, background commands, and transcripts           |
| [Files](files.md)             | Artifacts, file results, channel file delivery, and attachments            |
| [Channels](channels.md)       | Telegram, shared channels, Feishu and Lark, and WeChat                     |
| [Extensions](extensions.md)   | Skills, MCP servers, the secret vault, `AGENTS.md`, and reload             |
| [Automations](automations.md) | Routines, schedules, and results                                           |

## Failure behavior

- **dex exits.** On a graceful exit, dex stops its background commands and
  stores `stopped`. After a crash, their rows show as `unknown` at the next
  start and the processes can remain. Native execution and business receipts remain in their
  separate SQLite stores. The next start registers interrupted child sessions before
  Harness resume and reconciles input admission and receipt outcomes. A stable
  request ID reacquires admitted work instead of creating another input.
  This does not guarantee that external tool actions occur once.
- **Admission succeeds but the receipt write fails.** Reconciliation submits
  the same request ID, reacquires the native handle, and writes the receipt.
  A pending receipt that never reached PiG is retried too.
- **Crash between execution creation and saving its reference.** An empty
  native conversation can remain. The next attempt creates a conversation.
- **A native watch ends or an agent reloads.** The SSE connection closes.
  The browser reconnects and receives a new snapshot.
- **Model or tool error.** The submission becomes `unanswered` with a reason.
  The transcript shows an error item. A tool call that ended without a result
  shows as failed.
- **Crash between a memory edit and its commit.** The engine commits the
  change as `Recover uncommitted memory changes` when it opens the memory
  again.
- **Dream fails or stops.** Its message becomes `unanswered`, and the next
  dream, 24 h later, reads the same messages again.
- **dex not running at an automation's time.** The next check runs the newest
  missed time when it is at most 1 h old; otherwise it records that time as
  skipped. It ignores the older missed times.
- **Telegram API error.** The bot retries `getUpdates` after 5 s, from the
  first update that it did not store. A failed reply part stays undelivered.
  The channel sender retries it with a delay from 5 s to 60 s and after a
  restart. Confirmed parts are skipped. Telegram errors 400 and 403 skip the
  rejected part, so an unreachable peer does not block other peers. A crash
  after remote success but before the local commit can duplicate one part.
- **WeChat API error.** A temporary receive or runtime read error retries after
  5 s without a settings change or cursor advance. Failed reply parts stay in
  durable delivery storage.
  A permanent destination rejection skips the part. Either `ret == -14` or
  `errcode == -14` pauses API calls for 1 h and sets status to `needs_login`.
  A fresh QR login clears the pause and wakes the sender. Pending replies
  remain available for delivery.

## Security

In `local` mode, agent tools (`read`, `write`, `edit`, `bash`) run on the host
without a sandbox, inside the selected project directory or agent workspace.
Bots bound to a remote environment use that host or sandbox's environment.
dex sends the configured 1Password credentials and only the vault values that
an individual command requests. The HTTP server listens on
`127.0.0.1:8787` by default. On loopback without DEX_TOKEN it has no
authentication. On any other host, or with DEX_TOKEN, every `/api/` request
needs the access token.
A chat channel must allow only known user IDs: the settings refuse a
Telegram bot without allowed users. WeChat captures the owner ID from QR
confirmation and rejects a login without that ID. Other WeChat senders never
reach the agent. The API refuses cross-site writes
(`http.CrossOriginProtection`), so another web page in the user's browser
cannot send messages or change settings.

`control.sqlite` holds provider keys, channel tokens, WeChat context tokens,
plugin source secrets, GitHub Pull tokens, and IMAP passwords as plain text. WeChat logs exclude credentials and remote error text. The
data directory is created with mode 0700. The API never returns a key or a
token.

Uploads: a file name with a path, a control character, or more than 255
bytes is refused. An upload ID must be a UUID in its normal form, so it
cannot leave the uploads directory. `GET /api/agents/{agentId}/uploads/{id}` sends PNG, JPEG,
GIF and WebP inline with their type, and every other file as
`application/octet-stream` with `Content-Disposition: attachment`. All
responses have `X-Content-Type-Options: nosniff`, so an uploaded HTML or SVG
file does not run in the dex origin.

A stdio MCP server runs on the control host as the dex user, without a
sandbox. It gets the explicit agent environment plus its own `env`.
`control.sqlite` stores any plain MCP credentials in declarations as plain
text, within the same trust boundary as provider keys. Use vault references
in `env` and `headers`. The Extensions API and `mcp_server_list` show status,
tool names, and the unresolved references. `mcp_server_get` hides plain
environment and header values and URL queries. A failed server shows a safe
error, such as an HTTP status or the name of a missing secret. To see the full
error, run the server by hand.

Every chat reads the memory, including Telegram chats of each allowed user.
A memory entry can carry a prompt injection from a web page or a file to
later chats; the rules tell the agent that memory is data, not instructions,
and git history shows each change.

Control does not log the Telegram token. Bot API errors drop the request URL,
which holds the token. Local tool and stdio MCP processes receive the login
shell environment and the configured 1Password credentials. A call or server
can override variables explicitly. The engine does not change the dex process
environment. The login shell starts from six process keys, so dex settings
such as `DEX_TOKEN` do not reach tools unless a shell rc file exports them.

The [vault](extensions.md#secret-vault) encrypts shared and agent-level
secrets with age. Only the control host resolves references. It passes values
in the process environment, not in command arguments. It masks exact values
in final tool results and command Monitor wake-ups. Local tools run as the dex
user and have the same access to the control files as that user.

## Limits

- Harnesses stay open until shutdown or reload of that agent.
- The memory repo is local; dex does not pull or push. Two chats that change
  the memory at the same time can land in one commit, under the source of
  the chat that settles first.
- An MCP server that drops its connection stays down until a reload.
- dex does not delete uploads. A file that the user removes before sending,
  or that a message never uses, stays in `workspace/uploads/`. The files are
  in the agent workspace, so the agent's tools can change or delete them; the
  transcript then shows the changed file, or a broken link.

## Simplification review

Keep the native execution boundary. The engine owns execution and transcript
state. Business services own receipts, channel reply delivery, child session links, and
settings. Business code calls typed methods and never reads execution SQLite.
The two stores retain separate responsibilities inside one process.

The API uses the automation service types and `settings.Agent` through
OpenAPI `x-go-type` declarations. The generated API depends on those services;
the services do not depend on HTTP types. This removes a second model and the
field conversion layer. The Go JSON tags must match the OpenAPI schema. HTTP
tests check empty arrays and optional fields at this boundary.

Provider responses keep their own API type. The service's provider input can
hold an API key. The response type cannot hold that key. Keep this conversion
as the boundary that prevents secrets from reaching the browser.

Keep input intents and channel delivery work in SQLite. A coalesced wake
signal triggers reconciliation; it does not carry work. Native request IDs
make repeated admission safe. The in-memory waiting set only joins outcomes;
it is not the source of execution state.

Keep one transcript projection in the engine. History, snapshots, and live
events use durable entries and the native Inbox. The live hub owns transport
and empty-chat attachment, with no second transcript or execution model.

The tests cover separate behavior and failure cases. Keep restart, duplicate
message, multiple browser streams, model errors, tool results, and DST cases.
Do not remove these cases to reduce the test file size. The schedule iterator tests cover time arithmetic in isolation.

## Session Monitors

The scheduler reads due rows in `control.sqlite` (`monitors`). The upgrade
renames the original Timer table in place and treats its rows as one-run
prompt Monitors. The existing due time, submission IDs, deduplication key,
and one-hour grace rule stay. There are no old tool or API aliases.

Main, Chat, child sessions, and tasks can use both kinds of Monitor from any
turn. Dreams have no Monitor tools. `monitor_list` and `monitor_cancel` act
only on Monitors in the calling session. Tool results add `schedule-` or
`command-` to the stored ID. Use the prefixed ID to cancel a Monitor. The
session can have at most 32 active Monitors across both kinds. Each Monitor in a chain of wake-ups advances the
depth. After six steps, wait for user input before setting another Monitor.

`monitor_set_schedule` takes `at` or `delaySeconds`, and a prompt. Use
`maxRuns` (1–100), `intervalSeconds` (at least 60 for multiple runs), and an
optional `expiresAt` for repeated checks. A check Monitor takes `checkCommand`.
Repeated and check Monitors have a maximum lifetime of seven days. `env` is
valid only with `checkCommand`. It overrides environment variables for that
check. Use non-secret values: dex stores these arguments.

`monitor_set_command` starts a [command Monitor](execution.md#command-monitors).
It can wake the session on exit or in batches of stdout lines. Use `wake: none`
for a command that sends no wake-up. A bot can run at most eight commands.

Checks use the target session's Bash environment, variables, and saved
project directory. Dex does not use a separate sandbox or give the check new
credentials. Exit `0` saves a baseline on the first success and queues only
when the SHA-256 of the combined output changes. Exit `10` queues a terminal
event even when the output did not change. The shell has a 20-second timeout.
Check output must fit in 16 KiB for comparison. Dex does not save or inject the
raw output. Timeout, oversized comparison output, and other nonzero exits
stop the Monitor with a visible failure. Each attempt counts toward the run
limit. Dex does not replay missed intervals or overlap checks.

A script claim is durable. On restart an unfinished claim stops with an
interrupted outcome, rather than rerunning a command whose effect is not
known. A committed baseline and the next due time survive restart. Dex
commits the result and any follow-up intent together with a stable request
ID. The scheduler still checks due Monitors in one serial batch. A finish can
wait for its own Session gate so it cannot race explicit Monitor cancellation.
Ordinary inputs do not use the scheduler. The normal Session dispatcher admits
the Monitor intent as a follow-up. Legacy Stop, Clear, cancellation, archive,
and changed target authority block stale results. Web Main turn-stop leaves
Monitors alone. Dex rechecks the target before native admission. Cancellation
withdraws any Monitor inputs that are still queued. It does not interrupt a
prompt that already started. An in-flight check can finish within its time
limit, but it cannot queue a new prompt after cancellation. Dex does not move
a scheduled check to a different session, environment, or project directory.
Routines use their own existing path.
