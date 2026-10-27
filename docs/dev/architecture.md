# Architecture

This document is for developers who change dex. It describes the subsystems,
their sources of truth, the message flow, and the failure behavior.

## Runtime overview

![Runtime boundaries and data owners](../../assets/architecture.png)

Source: [architecture.puml](../../assets/src/architecture.puml).

dex runs HTTP, channels, business services, and agent execution in one Go
process. Business services call `internal/engine` directly. The engine uses
[PiG](https://github.com/MichaelKinsy/PiG) for durable execution, model access,
and MCP. The browser receives typed transcript events through SSE.

The macOS desktop build in [`desktop/wails3`](../../desktop/wails3/) uses Wails 3 as a window host. It reuses an existing Dex HTTP service with the same data directory or starts the same server on a loopback port. The desktop build keeps the OpenAPI and SSE contracts shared with browser clients.

## Runtime modules

The Go binary embeds the built web UI. Node and Bun are development tools
for the web UI; the installed service needs neither runtime.

The [module index](../../.agents/rules/implementation.md#modules) defines
package ownership. `internal/app` connects the services. Business services call
`internal/engine`; platform adapters use `internal/channel` for commands and
durable reply delivery. The engine does not import dex business packages.

Tools and stdio MCP servers can start child processes. Agent execution itself
stays in dex. There is no backend subprocess or serialized internal protocol.
See [ADR 0011](decisions/0011-native-pig-runtime.md).

## Service and updates

`cmd/dex` runs the foreground server or manages a per-user launchd or systemd
service. A host that is not loopback makes the API require the access token
([ADR 0007](decisions/0007-network-access-token.md)). Definitions store serve arguments, PATH, and a
configuration record that permits partial reinstall across template changes.

`internal/update` verifies GitHub release assets, stages beside the executable,
and retains `dex.previous`. `chat.Service` owns a two-minute maintenance lease.
The engine closes admission and checks active operations, durable work, and
pending settlement and memory commits. A busy check releases the gate and
refuses maintenance. A short global receipt gate orders receipt commits and
lease confirmation. After lease confirmation, web input returns `409` before
the service writes a receipt. A web receipt committed before confirmation can
return `202` and remain pending until release. Channel input can also retain
a pending business receipt. Reconciliation resumes admission after maintenance.
The client keeps the same request ID when it retries.
A process restart clears the in-memory lease.
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

See [service commands and recovery](../../README.md#run-and-manage-dex) and
[ADR 0006](decisions/0006-service-and-update.md) for service and update
decisions. [ADR 0011](decisions/0011-native-pig-runtime.md) replaces the
backend readiness and maintenance lifecycle.

## Sources of truth

| Data                                 | Owner             | Location                                                                   |
| ------------------------------------ | ----------------- | -------------------------------------------------------------------------- |
| Agents, chats, submissions, settings | control           | `<data dir>/control.sqlite` ([migrations](../../internal/db/migrations/))  |
| Transcripts, runs, tool calls, usage | engine (PiG)      | `<data dir>/agents/<id>/state.sqlite`                                      |
| Agent files                          | the agent's tools | `<data dir>/agents/<id>/workspace/`                                        |
| Uploaded files                       | control           | `<data dir>/agents/<id>/workspace/uploads/<upload id>/<name>`              |
| Memory (git repo)                    | the agent's tools | `<data dir>/memory/`                                                       |
| Skills and MCP servers               | the user's files  | `$HOME/.agents/`, `<data dir>/.agents/`, `<data dir>/agents/<id>/.agents/` |

Control also owns internal delegation records (`tasks`). Each record
has one task conversation that is read-only on the web and one input. Background
execution keeps bash and all coding tools. The engine
owns its transcript and execution records. Business services do not query execution SQLite.
The records retain the source submission, status, and report. A cancelled
status records a cancellation request. It does not prove process exit. There
is no user goal lifecycle or automatic retry chain.

Each agent has one main conversation with channel key `main`. The unique
agent/key constraint makes `GET /api/agents/{agentId}/chats/main` safe to repeat.
It creates the control record on demand. Messages use
`POST /api/agents/{agentId}/chats/{id}/messages` and the agent's settings.
`POST /api/agents/{agentId}/chats` takes no body and returns a writable
Chat session (`201`).
Its channel key starts with `chat:`. One SQLite write reuses the newest Chat
with no ordinary message, or creates one. Concurrent opens reuse the same row.
The first message and its title commit together. The title has at most
60 characters. Longer text uses its first 59 characters and an ellipsis. A file-only
message uses the first file's name. Each Chat has its own execution conversation.
It uses the bot's settings, instructions and shared memory. It does not read
main's model context or send its answers to main.

`GET /api/agents/{agentId}/chats` lists session summaries
by `updatedAt` descending, then ID descending. `updatedAt` is the latest
conversation creation, input creation or settlement, or task start or end time.
Task times apply only to the task's own conversation. List and detail use the
same rule. The list excludes main, old channel conversations, dreams and
Chat sessions with no message. Task rows add `task: TaskSummary`, without report bodies. Running task rows also include the latest observed tool action, activity time, and earliest currently running tool start time when the task conversation view is already open in the engine. These three fields are in-memory only and disappear when the task settles; a restart interrupts running tasks and does not restore them. A structured report adds `reportOutcome` (`done`, `blocked`, or `failed`) regardless of task status. The activity tracker reads existing native views and never opens a Harness or `state.sqlite` while serving this list. The tracker observes activity only while a conversation view is open, so a cold agent has no activity data. A running task is considered potentially stalled by the client after five minutes without a tool or thirty minutes in a tool; these thresholds should change only when measured false positives or user needs justify it. The query does not read reports or transcripts. The active count includes tasks with status queued or running, and other busy sessions. Each row counts once. The legacy Sessions route remains readable for saved links.

Channel input enters the agent's main conversation. Chat and automation
contexts stay separate. See [Product](../product.md#scope) for session behavior,
page navigation, and channel setup.

The web keeps visited main timelines mounted and `inert` when hidden. Their
streams, composer drafts, and uploads survive navigation. Browser checks must
skip `[inert]` subtrees. Instructions drafts live above the routes in a map by
agent ID. Saving one agent cannot change another agent's draft or query data.
A browser reload discards these drafts.

Every enqueue writes `submissions.origin`. Web input in main and Chat uses `web`.
Channel input uses the exact peer, such as `telegram:7`, and always enters main.
Automation, task input, and dream input use `automation`, `task`, and `dream`.
A task result copies its source submission's origin in the same transaction
that saves the report and inserts the result. A result chain thus keeps the
first input's origin. Existing rows use `unknown`. The durable channel sender
routes ordinary replies and task results by origin. Web input sends no channel reply.
The engine stores origin with native input metadata. One projection exposes it
in queued input, snapshots, live events, and history pages. The web shows the
channel name beside each channel input.

The default data directory is `~/.dex` (`DEX_DATA_DIR`). Only the engine opens
each agent execution store. Run one dex process per data directory.

## Agents and HTTP API

One owner can create several agents. `GET /api/agents` returns
`{agents: [Agent]}` in creation order. Each list entry has `activity` with
`activeSessions`, `runningChannels`, and `enabledAutomations`. One SQL query
counts sessions and automations across all agents. A task counts when queued or
running; other visible sessions count when they have pending input. Each session
counts once. Channel counts come from the app registry. The web polls this
list every three seconds and shares its cache across agent pages. The same
response includes per-agent `inboxUnread`, `inboxFailed`, and `routineFailed`.
These fields come from the activity SQL query. The rail sums the unread counts
for the global Inbox. No extra fast poll is needed. `POST /api/agents` accepts a name,
provider, model ID, thinking level, and optional instructions. It returns the
agent with a UUID v7 ID (`201`). The ID is read-only. `GET` and `PUT` on
`/api/agents/{agentId}` read and save one agent. Names and models use the same
validation for creation and updates.

Agent resources are under `/api/agents/{agentId}`: main, chats and their
settings, messages, clear, history, abort and events; task details; uploads;
extensions and reload; automations and their run and runs endpoints. An unknown
agent or a resource owned by another agent returns `404`. Upload storage uses
`<data dir>/agents/<agentId>/`. Control builds the path store for each request.
Each live stream uses `engine.Ref{AgentID, ID}` for watches and history.
Execution IDs can be equal in different agents.

Session, health, maintenance, models, provider settings, and
`/api/automations/preview` stay global. Separate services own global settings
and agent state. The settings lock orders provider saves and local engine
configuration. Each settings request releases that lock before it waits for
its own discovery operation. The agent lock orders agent writes,
credential ownership checks, channel changes, and setup sessions across all
agents. Agent model checks distinguish pending discovery from an invalid model.
The lock order is agent, then settings. Settings never takes the agent
lock. Each service writes separate rows. Creating or selecting an agent does
not configure providers.
One scheduler job checks automations across all agents and posts each prompt
with the saved agent ID. See [Extensions](#extensions) for the reload lifecycle.

The web UI uses the nested agent routes, including live events and upload
links. Agent resources require an explicit ID, including the default agent.
The old un-nested API routes return 404. `GET /api/settings` returns only global
settings and has no agent or channel fields. Channel setup and status use
`/api/agents/{agentId}/channels`, with `/telegram`, `/wechat`, and `/feishu`
for changes. The old `/api/settings/{platform}` routes return 404.

## Memory

The memory is a git repo in the
[Agent Memory Repo](https://github.com/AgentMemoryRepo/agentmemoryrepo) format
at `<data dir>/memory/`. It belongs to the user, so all agents and channels
share it. See [ADR 0004](decisions/0004-agent-memory-repo.md).

- **Read.** The [`memory` extension](../../internal/engine/memory.go) adds three
  system prompt sections: the memory rules (the repo path, the entry format,
  and the chat's `source`), shared `MEMORY.md`, and that agent's
  `agents/<agentId>/MEMORY.md`. Other agents do not load this last section.
  They render before each model
  request, so a change applies from the next request, also inside a run.
  PiG sends a section again only when its text changed. The agent
  finds the other files with grep and the `[[path]]` links.
- **Write.** The agent changes the files with its edit and write tools,
  without approval. It does not run git. When a submission settles, the
  engine commits all changes as `Update memory from <source>`. The source is
  `dex:<agentId>/chat/<chatId>`, or `dex:<agentId>/dream/<dreamId>`.
  Old `dex:chat/<id>` sources remain valid and mean the default agent.
  Owner facts go in shared `MEMORY.md` and its topic files. Role experience
  and ways of working go in `agents/<agentId>/`, with its own `MEMORY.md`
  entry point. This keeps one bot's habits out of the owner's preferences.
- **Dream.** The [scheduler](../../internal/scheduler/scheduler.go), a
  wrapper of gocron, runs [`dream.Dreamer`](../../internal/dream/dream.go)
  at start and then every hour. The schedule only causes a check; the
  `dreams` table decides. One Dreamer checks every agent. When an agent has a
  model, none of that agent's submissions are pending, no dream is pending
  across all agents,
  the last dream started 24 h ago or more, and the oldest settled message that
  no dream read is 24 h old or more, the check starts a dream. A dream is a
  hidden chat (channel key `dream:<id>`). Its system prompt holds the dream
  instructions and the privacy and language parts of the agent instructions.
  Its message holds the new messages as JSON lines, in settle order, with
  local times: up to 500 messages or 100,000 bytes. A text longer than 4,000
  characters keeps its first 3,000 and last 1,000 characters and is marked as
  cut. The agent adds what repeats across its chats. It tidies its own folder
  and shared entries whose chat sources all belong to it. Entries with other
  agents' sources, including mixed sources, can only gain `uncertain: yes`.
  Shared entries with no chat source stay unchanged. The
  `dreams` table records the last message that each dream read; the next dream
  starts after that agent's newest done dream.

## Extensions

Skills, MCP servers, and `.env` come from three levels: home (`$HOME/.agents/`),
dex (`<resolved data dir>/.agents/`), and agent (`<data dir>/agents/<id>/.agents/`).
Narrower levels win. Paths are fixed before `.env` is read. Equal real paths
are read once as the narrower level. See [ADR 0010](decisions/0010-extension-levels.md).
Skills have a fourth, narrowest level: `.agents/skills` under the
conversation's cwd. The skills prompt section reads that cwd from the request's
execution environment, so it follows the conversation. Chat sessions store
their `cwd` in `control.sqlite`. Empty uses the agent workspace. Every submit sets or clears the native cwd from that row. Tasks
copy the parent's cwd when they are created. The `project` prompt section
states the current execution directory.

`GET /api/directories` lists visible host directories with the same path validation
as project changes. It follows directory symlinks, marks Git repositories, sorts
names without case, and returns at most 500 entries. It requires the same API
authentication and never returns file contents. `GET /api/agents/{agentId}/projects`
reads distinct conversation paths in activity order with one control query.
It checks each path and returns up to eight existing directories.
The Chat picker uses these routes because a remote browser cannot select host paths.

`PUT /api/agents/{agentId}/chats/{chatId}/project` changes the cwd for the
next message. Only `chat:` conversations can set it. The API and `set_project`
tool share one path validator: trim, expand `~` or `~/`, require an existing
absolute directory, and clean the path. Empty clears the override.
The foreground tool writes control state before it commits native
`harness.Configure`, so later tools in the same run use the new directory.
Replay writes the same value. Main and automation have the `set_project` tool but return
an error. Tasks and dreams do not have it. The Chat header refetches on tool
completion and run completion. For a project cwd, native input uses absolute
upload paths. The transcript keeps workspace-relative upload paths.

The Extensions API reads the agent workspace. Each level is walked like Pi's skill loader: a directory with
`SKILL.md` is one skill and the walk stops there; hidden directories and
`node_modules` are skipped; symlinked directories are read once. The front matter
`name` wins over the directory name. A second skill with the same name in one
level is a warning, not an error, so it does not block MCP Reload.
Each agent has its own environment snapshot for tools and stdio MCP servers.
Invalid `.env` or MCP files prevent all MCP startup for that agent. Invalid
`.env` also removes all file environment values from that snapshot.
The [engine extension loader](../../internal/engine/extensions.go) connects
servers before it resumes the agent Harness. It installs prompt sections for
the extension guide and current skills. MCP tools use the names
`mcp__<server>__<tool>` and deferred tool discovery. The guide lists fixed paths,
file formats, environment use, secrecy, and Reload. It contains no environment
key names or values. Environment edits do not change its text.

`PUT` and `DELETE /api/agents/{agentId}/extensions/env` call the engine's
write-only environment methods. Edits are serialized per path and replace
the file atomically with mode `0600`. They preserve unrelated lines and refuse
invalid files. Home is read-only. Saving does not reload. The file is the only
store.

`GET /api/agents/{agentId}/extensions` reads file metadata. Before the agent
opens, servers are `idle`. `POST /api/agents/{agentId}/extensions/reload`
validates the selected agent and requires idle execution across all agents.
The idle check and registration of the selected replacement are atomic. Reload
does not hold the global maintenance pause. After preflight succeeds, other
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
See [ADR 0012](decisions/0012-agent-lifecycle-isolation.md) for this lifecycle
change and [ADR 0011](decisions/0011-native-pig-runtime.md) for native execution.

The task extension adds delegation tools and a `task-rules` section to
foreground conversations, including main, Chat and automation sessions.
Background executions and dreams have no delegation tools. A separate
extension gives foreground conversations and background executions the `coding-skill`
section. Go embeds the full
[built-in coding skill](../../internal/engine/skills/coding/SKILL.md) in the engine.
It does not copy the skill into a user directory or add a new extension scope.
The skill keeps implementation, fixes and tests in the main context. It
requires a repository path, project rules, independent review, tests of the
final revision, and evidence in the report. It requires serial writes
or separate worktrees for tasks that change the same repository.

## Automations

Main and writable Chat conversations have five routine tools:
`list_routines`, `get_routine`, `create_routine`, `update_routine`, and
`run_routine`. Typed engine callbacks call the automation service. The model
cannot select another bot. Business validation checks the calling execution
and input before it exposes routine data or changes. Automation, task, and
dream conversations cannot use these tools.

Updates are partial. An enabled-only update pauses or resumes a routine
without replacing its prompt or project directory. Creation inherits the
calling conversation's project when `cwd` is omitted. A run result includes
its chat ID so the agent can link to the saved transcript. Tool results name
the duplicate-finding flag `repeatedFinding` to distinguish it from execution
counts. The HTTP API keeps its `duplicate` field. The tools do not provide deletion.

Each mutating tool call uses its bot ID and native tool task ID as a stable
operation key. The control database commits its operation receipt with the
routine change or run input. Replay returns the saved result. It does not
repeat the write or advance the schedule. A new mutation requires an active
user input; a saved receipt can still be read after that input settles.

An automation runs a prompt on a schedule; see
[ADR 0005](decisions/0005-automations.md).
[`automation.Service`](../../internal/automation/automation.go) stores
automations and their runs in `control.sqlite`. One scheduler job calls its
`Check` every minute for all agents. Each row supplies its owning agent ID.
`Check` posts the prompt of each automation whose
`next_due_at` has come, as a message to its chat (`automation:<id>`, or
`automation:<id>:<run id>` for a new chat each run), and moves `next_due_at`
past now. The message uses the normal durable delivery.

Run records and durable message intents commit in one control transaction.
The run ID is the input request ID. A crash before commit leaves neither
record. A crash after commit leaves an input that normal chat reconciliation
can admit. If the schedule cursor did not advance, the unique automation/due
key prevents another input for that scheduled time.

An automation can store a project `cwd`. The API uses the Chat path validator.
Each run sets or clears its chat's cwd before it stores the input. Project
skills therefore follow the run directory, including when a chat is reused.

Only automation conversations have `report_check`. The tool stores an explicit
`changed`, `unchanged`, or `failed` outcome, a summary, and an optional change
key. The report is provisional while the run is active. Submission settlement
and the final run outcome commit together. Missing reports and execution errors
produce `failed`; answer text cannot imply `unchanged`. Old terminal runs
without reports keep an unknown outcome. Old started runs without an input
migrate to `failed`. Skipped runs have no outcome.

A completed report with the same nonempty change key as an earlier
completed report has `duplicate: true`. Both run records remain. Deduplication
records repeated findings; it does not prevent or undo actions. The runs API
exposes these fields in routine check history. The web calls automations
Routines; API paths and code names remain `automation`.

`automation_runs.read_at` and `dismissed_at` hold Inbox state. A separate table
would add a one-to-one record with no independent lifecycle. `GET /api/inbox`
uses one query across all agents. It joins runs, routines, agents, and inputs.
Only settled `changed` and `failed` runs without a duplicate mark qualify.
The query excludes dismissed results unless `includeDismissed=true`.
Read and dismiss endpoints keep the first timestamp and return 404 for runs
that do not qualify. Repeated writes are safe.

The web uses the shared list for result details and history links. It refreshes
the list every 10 seconds while an Inbox page is open. Read and dismiss writes
invalidate the list and agent activity. Dismissed details remain readable by URL.
Continue in chat uses the existing create Chat, project, and message APIs in
that order. Its first message quotes the routine name, outcome, summary, and
run transcript reference. A retry in the same detail view keeps the Chat and
message request IDs.

## Settings

The web UI edits all settings; see [ADR 0002](decisions/0002-settings-in-control-db.md).
[`settings.Service`](../../internal/settings/settings.go) owns providers, model
checks, and the access token. [`agent.Service`](../../internal/agent/agent.go)
owns agent settings, activity, and channel configuration. `chat.Service` saves
chat settings with an injected validator. Each change stays in `control.sqlite`:

| Setting                                 | Table               | Applied by                                     |
| --------------------------------------- | ------------------- | ---------------------------------------------- |
| Bot model, thinking level, instructions | `agents`            | the next `submit` of each chat                 |
| Chat model, chat thinking level         | `conversations`     | the next `submit` of that chat                 |
| Providers                               | `providers`         | the engine catalog, and again at process start |
| Telegram token, allowed users           | `telegram_settings` | a restart of the bot                           |

`PUT /api/agents/{agentId}` saves the name with the other bot settings.
The API trims spaces, then accepts 1 to 40 Unicode characters. The web uses the saved name in
the sidebar, the timeline header, and the composer.

Main and Chat sessions use the agent's model and thinking level. They have no
session override. `chat.Service` resolves settings at each `submit`. Channel chats cannot
change their settings (403). An automation chat can; an automation that
reuses its chat then runs with the chat's settings. The thinking levels and
the `reasoning` and `input` of a model come from PiG AI model definitions; the model adapter sends the nearest level that the model
supports, and `off` to a model without reasoning.

The user adds any number of providers, and then chooses one model from all of
their models. A provider is one of:

- **Built-in**: a PiG AI provider (Anthropic, OpenAI, OpenRouter, and more) with
  an API key.
- **Custom**: an ID, a base URL, an optional API key, and one of the APIs
  `openai-completions`, `openai-responses`, or `anthropic-messages`. Its
  models are the listed model IDs, or else the ones that the endpoint's
  `/models` (OpenAI) or `/v1/models` (Anthropic) returns. A custom provider
  without a key sends the key `none`, for local servers.

For task tools, custom `openai-responses` providers send `strict:false`.
This keeps optional parameters optional at Responses endpoints.

The engine keeps provider configuration and credentials in memory through its
model catalog. The settings service registers saved providers at process start
and discovers dynamic models in the background. Each network request has a
30-second deadline. A settings change keeps each unchanged provider and its
models. Changed providers cancel their previous discovery. Only the current
provider's discovery can supply its models and error. A cancelled HTTP request
ends that request's wait; discovery continues until completion, replacement,
deletion, or shutdown.
Provider errors do not expose secrets. A custom provider whose model discovery
fails stays saved; the API returns the failure. Saving the same settings retries
a failed discovery. The agent has no model until the user chooses one. New message
admission requires a selected model. Receipt
recovery first looks up the native request ID, so clearing or changing the
current model cannot replace an existing native answer.

## Native execution boundary

`internal/engine.Agents` owns one concrete execution service. Business code
uses typed Go calls and `Ref{AgentID, ID}`. An execution ID names a PiG
conversation, while a chat ID names a business record in `control.sqlite`.
`conversations.execution_id` connects the two records.

- `Create` creates the execution conversation. `Submit` durably admits an
  input with a stable request ID and returns a `Submission`. `Submission.Wait`
  waits for the outcome. Canceling that wait does not cancel the execution.
- `Abort`, `CheckIdle`, and `Reset` operate on one execution reference. Reset
  uses a stable request ID. `Recover` registers task conversations that must
  be isolated before Harness scheduling resumes.
- `History(ctx, ref, before, limit)` returns typed `History`. `Watch(ctx, ref)`
  returns a typed initial `Snapshot` and an `Events` channel. `Watch.Close`
  cancels only that subscription. `Quiesce` and `Resume` control maintenance
  admission. Reload does not use them. `Close` ends resource lifetime.

The engine serializes input, reset, and abort operations per execution. Native
input metadata contains request ID, original content, origin, attachments,
source, and optional task ID and title. `dex.user` and `dex.task_result` entries
keep display facts with the durable input. `dex.withdrawn` entries preserve
queued inputs that Stop removed. Withdrawn entries contribute no model text.

Task tools call injected typed handlers in `internal/chat`. The engine does
not import the HTTP API package. Frontend `Item`, `Event`, `Retry`, and
`History` types live in the engine and keep the HTTP JSON field names. JSON
encoding occurs at the HTTP boundary.

## Message flow

![Message flow](../../assets/message-flow.png)

Source: [message-flow.puml](../../assets/src/message-flow.puml)

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
   must not suppress that retry. Reconciliation scans receipts in insertion
   order. It stops later input of the same chat after a retryable failure.
4. The engine runs the model and tools. Each browser subscription receives
   typed transcript events through `live.Hub`. Queued input comes from the
   native Inbox, not from a second control transcript.
5. The chat service waits on the native handle and stores the outcome in the
   business receipt. The channel outbox reads settled receipts and records
   confirmed reply parts. A restart reacquires native outcomes by request ID;
   it does not restart a second execution loop in control.

A client retry with the same UUID reuses the input intent and native admission.
The two stores do not share a transaction. Stable request IDs and receipt
reconciliation cover the gap between their commits.

## Internal delegation

Each conversation runs one main agent loop. Main and each Chat have separate
contexts. Skills, tools and the project directory provide capabilities. The main agent does ordinary work
itself. It uses `spawn_task` only for independent parallel work, long commands
whose output would flood its context, and independent review. It uses
`list_tasks`, `get_task`, and `cancel_task` to manage that work.
Each spawn creates one execution with fixed model and thinking settings.
There is no goal acceptance lifecycle. The main agent checks saved records
before it reports progress or starts another execution. The main agent owns
implementation, independent review, fixes, final tests, and the user report.
Background conversations cannot call delegation management tools. They can call
`report_task` and use all coding tools, including bash. Control
checks this boundary as well as the engine's tool list.

Creation checks source ownership, pending input, and deduplication in one SQLite write transaction. The durable spawn key is the agent ID plus PiG's tool task ID. Replay returns the same execution. Each agent admits up to 20 queued, admitting, or running executions. Four tasks run per bot and eight tasks run across all bots. The scheduler preserves FIFO order per bot and holds modifying tasks exclusively by cwd across all bots; read-only tasks may share a cwd. Cancellation withdraws queued work or requests cancellation of running work. The main agent handles failure, retry, and continuation from saved artifacts.
The prompt requires a record check before progress reports or follow-up work.

The goal is the content of the task's own submission. Each task also saves
optional `constraints` and optional `doneCriteria` at creation. Spawn replay retains the original contract. The engine prompt
includes that contract. Constraints are instructions; they do not restrict tools
or provide a sandbox.

Only background tasks have `report_task`. It stages one report with outcome
`done`, `blocked`, or `failed`, a summary, a cause, reproduction commands with
an explicit `ran` flag, files, and open items. As with `report_check`, the saved
report is provisional until execution settles. Replaying the same report is
safe. A different report cannot replace it. `get_task` returns the full contract
and structured report by task ID. `hasStructuredReport` marks report presence.
`goalMet` requires successful execution and a structured `done` report. It is
a worker claim that the main agent must check against artifacts and tests.
An execution without `report_task` retains its final answer and has
`hasStructuredReport: false` and `goalMet: false`.

Normal completion (`done`), execution failure (`failed`), and recovery
interruption (`interrupted`) create a durable `task_result` submission in the
same control transaction as settlement. The parent input keeps the existing
4,000-character report truncation. The main agent reads the full report with
`get_task` before follow-up work. For a follow-up such as “OK, fix it”, it
reads the report again by ID and checks the current files before editing.
The result
enters the native follow-up Inbox even when its source is busy. There is no
reject-when-busy mode or business retry queue for task results. Stop and
directed cancellation set active executions to `cancelled`. Cancellation creates
no result turn. Startup recovery reads each live task's native terminal outcome
before any Harness resumes. A saved native completion or failure follows the
normal settlement path and retains its report. An unfinished execution becomes
`interrupted`, with durable abort intent and one parent result. That result tells
the main agent to report the interruption and warn that side effects may exist.
A terminal record and its saved report stay unchanged when a late
execution outcome arrives.

Control passes a trusted input kind, task ID, and task title to the engine.
The engine stores internal results as `dex.task_result` entries. The public
projection replaces each result input with a `task` mark that contains no
report text. The mark needs no task lookup in `live.Hub`.
The web groups output after that mark until the next user or task item.
A failed result reply cannot retry the user message before the mark.
Neither layer uses message text or an ID pattern to hide results. Ordinary
user JSON still appears and can use the normal Retry action. Task conversations
stay absent from dream inputs. The bot has no Sessions tab. The human can read their chat,
history, and normal SSE stream. Web input, settings, clear, and abort are refused.
Dream input selects only ordinary messages, so internal results in a source session
cannot enter memory as user text.
`GET /api/agents/{agentId}/tasks/{id}` returns one record with its report, times, and conversation
ID. The task and session reads check the agent. Model task tools keep their
parent conversation scope. There is no `GET /api/agents/{agentId}/tasks` list, HTTP task control,
or separate task stream. Each writable session polls session summaries every 3 s. The legacy Sessions
route remains available for saved links.
Task details poll every 3 s while queued or running. Terminal details stop polling.
Task details use the normal read-only chat, history, and SSE paths for Work.
Successful `spawn_task` items carry the ID from the saved tool result. Result
marks carry the ID and title saved in the native input metadata. The web links
both labels by ID to `/a/{agentId}/tasks/{taskId}`, a read-only task page.
It never matches tasks by title. The four bot tabs are Chat, Routines,
Extensions, and Settings. Task records need no list tab.
The source session's normal SSE stream carries result marks and replies.
A task started in Chat keeps that Chat as its parent. Its result returns to
that Chat, with the source input's origin. Main does not receive the result.

Result delivery uses the existing submission lifecycle. A provider failure
leaves the result unanswered. Control never changes a settled submission back
to pending, so no retry counter or delivery recovery state is needed. The
main agent can read the saved report and choose the next action. A user Stop
withdraws queued native results. The transcript shows their withdrawn state,
and the saved report stays readable. A later greeting cannot revive those results.
A new execution must check prior artifacts and side effects.

Web Stop covers only the selected writable session, main or Chat. Task,
old channel and automation sessions are read-only on the web. Messages, Clear
and Stop return 403 for those kinds.
Channel `/stop` calls main Stop. It covers main input from every entry and
tasks started in main. Chat, automation and other agents stay outside that scope.
Channel `/new` calls main Clear with the same busy check and reset ordering.
It does not cancel background work. The result keeps the work's original peer.
Stop withdraws pending ordinary input and results. The same transaction sets
active background executions to `cancelled` and withdraws their own input.
Stop and spawn acquire the SQLite writer before they read: spawn either commits
first and is cancelled, or fails because its source is no longer pending.
Submission, Stop, Clear, and reconciliation share a delivery lock per agent.
Cancellation cannot pass an input that control still intends to submit.
A slow admission or reload for one agent does not hold another agent's delivery
lock. The short global receipt gate orders maintenance confirmation, as described
in [Service and updates](#service-and-updates). Stop then requests cancellation
through the engine for each execution reference. Pending cancellation intent
stays in business storage until native abort succeeds. If model discovery is
pending, Stop returns after it saves that intent. Reconciliation completes
cancellation before it admits later input. Both cold recovery and ordinary
Stop atomically withdraw queued input and save passive transcript entries.
Those entries retain origin, attachments, and task labels without adding model
context. Reconciliation retries cancellation after an error or restart.
Late reports cannot change a terminal record or wake the model.

`POST /api/agents/{agentId}/chats/{chatId}/abort` returns `{status: "ok"}` after withdrawal and the
cancellation requests. The composer keeps Stop available for queued input,
an active answer, and active background work started by that conversation.
Tasks from other conversations count in the bot's badge but cannot enable
that session's Stop. Task summaries expose `parentConversationId` for this check.
Stop needs no model. During a
reconnect, the chat API's busy state keeps Stop available before the snapshot.
The reply confirms the request. It does not prove process exit. Processes and
remote work can continue. Cancellation does not undo file writes or remote
actions. A directed cancellation of a completed execution leaves its report
and saved result unchanged.

At process start, control calls `Agents.RecoveryOutcome` with each live task's
execution reference and stable request ID. The engine reads committed native
submission and answer records through PiG storage without opening or resuming
a Harness. Control never opens `state.sqlite`. Native terminal outcomes settle
through the normal task transaction. Only unfinished executions become
`interrupted`. Their abort intent, report, task receipt, and parent result commit
together. Control then registers pending abort references with `Agents.Recover`
before any Harness can resume. Recovery isolates those conversations, including
cancelled tasks whose abort did not complete. A queued execution that never
started can start for the first time. Repeated restarts retain one task record
and one result input with request ID `task:<id>:result`. Native admission deduplicates
that request ID. Completed results already saved for delivery retain their
receipt state. Durable spawn replay returns the original task record.

The SDK owns cancellation of run invocations and tools. Control does not track
process trees or confirm exit. A normal local or MCP tool return can settle
execution. An aborted MCP call ends the local wait and requests remote
cancellation. It does not prove that the remote job stopped. Before another
execution, the main agent must check reports, artifacts, and prior actions.

Tools use the local `NodeExecutionEnv`, without a sandbox. CLI tools must be
logged in and run in the foreground with non-interactive options. Detached
processes and daemons are outside the supported lifecycle. Bash has no default
timeout. Its visible output keeps at most the last 2000 lines or 50 KiB,
whichever limit comes first; complete output goes to a temporary file.
A durable checkpoint does not attach a CLI after restart or guarantee that
external actions occurred once.

## Attachments

1. The browser sends each file as `POST /api/agents/{agentId}/uploads` (multipart, one file,
   at most 20 MB). [`upload.Store`](../../internal/upload/upload.go) writes
   it to `workspace/uploads/<UUIDv7>/<name>` and returns its ID, name, size,
   type (from the content) and workspace path. The directory is the only
   record of an upload.
2. A message carries at most 10 upload IDs. Control checks each ID and stores
   the attachments as JSON with the submission, so a replay sends the same
   list.
3. The engine gives the model the text and then `Attached files:` with one
   workspace path per line. The model reads a file with the read tool, which
   also prepares images for a model with image input. The `dex.user` entry
   keeps the attachments, so the transcript shows the files and the text
   without the list.
4. `GET /api/agents/{agentId}/uploads/{id}` serves a file for the transcript.

## Transcript subscriptions and history

The HTTP contract is [`api/openapi.yaml`](../../api/openapi.yaml): schemas
`Item` and `ChatEvent`. [ADR 0011](decisions/0011-native-pig-runtime.md)
updates the backend implementation of ADR 0003.

- The [Go projection](../../internal/engine/view.go) renders immutable native
  conversation revisions. Each revision includes entries, the live partial,
  tool state, retry state, and the full Inbox. The renderer also serves history.
- A text-only increase sends `append`. Other item changes send `item`.
  Removal, reordering, a committed partial ID change, or a new history boundary
  sends a replacement `snapshot`. Running and retry changes send `run`.
- Each subscriber has its own native watch. Snapshot capture and subscription
  share one revision. A reconnect needs no replay. Closing one browser tab
  cannot stop another tab's subscription.
- Queued user input comes from the native Inbox. Withdrawal writes passive
  `dex.withdrawn` entries that display the input and an aborted error. Origin,
  attachments, and task labels come from durable metadata. The live hub has no
  queued or failed input overlay and makes no label lookups.
- A slow subscriber can buffer at most 256 frontend events. Overflow stops
  the watch so the browser can reconnect. Execution never waits for that reader.
  SSE sends a comment line every 15 s.
- `live.Hub.Changed` only helps a stream attach when an empty business chat
  receives its first execution reference. The hub encodes typed engine events
  as JSON at the SSE boundary.

`POST /api/agents/{agentId}/chats/{id}/clear` uses the agent delivery lock and a SQLite writer
transaction to check pending input and insert `context_reset` together. The
engine also checks its active run and Inbox. A refused request stores no reset.
`Agents.Reset` submits a passive `pi.reset` entry with a stable request ID and
`head: self`. A retry adds no second reset. Reset entries keep their time in
data. Memory files remain available. Follow-ups keep one input per answer;
they do not merge all queued input or steer the current turn.

The Clear context action is in the composer. The server alone decides whether
work is active. A 409 shows a warning and keeps the same request ID for a retry.
The action is hidden when the last non-compaction item is a reset or is absent.

A snapshot contains only the active native context. Reset entries show dividers.
Compaction entries stay in the data but have no divider. `historyBefore` points
to the head entry. The browser requests
`GET /api/agents/{agentId}/chats/{id}/history?before=<entry ID>&limit=100` on demand. The engine
reads `Conversation.Entries` before that exclusive cursor and returns items
in entry order with `nextBefore`. A page can extend to the previous assistant
or input so a tool call keeps its result. Committed assistant block IDs use
`e<entry ID>:<block index>`. User IDs remain submission IDs; tool IDs remain
call IDs. The browser merges overlaps by ID and keeps queued input last.
Older withdrawn input loads from the same durable entries through history.

## Telegram

[`telegram.Bot`](../../internal/telegram/telegram.go) is on when the settings
have a bot token. It reads messages with `getUpdates` long polling,
so dex needs no public URL.

- Only private chats can enter the agent. The bot ignores users outside the
  saved allowlist. A saved token with no allowed users waits for owner pairing.
  Earlier versions accepted group messages from allowed users. The bot now
  ignores all non-private chats, including messages from allowed users.
- The Telegram channel page creates a link with the bot username from `getMe` and a
  random code. The code expires after 10 minutes. `telegram_settings` stores
  the code and expiry. Pairing is available only while the allowlist is empty. One atomic SQLite
  update sets the owner as the only allowed user and clears the code.
  Invalid or expired codes create no user or chat. A new link or settings
  change cancels the previous code. Manual user-ID entry remains available.
  An allowed user who repeats a pairing command receives an already-paired
  reply, including after a lost acknowledgement or restart.
- The bot registers `/new`, `/stop`, and `/help`
  with `setMyCommands`.
  The channel manager owns command execution and deduplication.
- Each update has a stable source key: `telegram:<bot id>:<update id>`.
  A repeated update does not create another submission.
- Goldmark parses Markdown into Telegram HTML. The sender splits at block
  boundaries and closes code tags in each part. Each part has at most 4096
  UTF-16 code units, including markup. A rejected HTML part retries as plain
  text. The shared durable sender retries final-send failures, including 429,
  with a delay from 5 to 60 seconds. Markup and escaped characters count
  toward the current splitter limit. This can produce more parts than
  Telegram requires. Keep these boundaries stable for stored delivery progress.
- Each pending submission has a native watch through `live.Hub`. The bot
  updates a private draft at most once per second. If drafts fail, the bot
  sends one preview and edits it. Typing refreshes every 4 seconds. Previews
  are best effort. Queued user items do not start answer segments. The final
  reply stops and joins its preview before delivery. Preview registration and
  final lookup use one lock; attachment downloads do not hold this lock.
- Photos use the largest size. Documents and photos pass through
  `upload.Store`, with a 20 MB limit. Captions become message text. The agent
  and web transcript use the same upload record.
- Polling checks the token through `getUpdates`. A transient startup failure
  does not stop the bot. Poll errors and recovery update runtime status in
  bot settings. Tokens do not appear in status reasons.
- Pairing and attachment-rejection replies skip permanent 400/403 failures
  so later updates can proceed. Transient failures leave the update for retry.

## Shared channels

The app owns a registry with one Manager per agent. Startup restores all agents' bots.
Creating an agent adds its Manager. Shutdown stops all Managers before the database closes.
Uploads and live previews use the bot's agent directory and stream. Chat notifications
wake all Managers; each Manager reads only its agent's replies and commands. This fan-out
is a limit: route notifications by agent if wakeups become costly.

Channel settings use `agent_id` as their primary key. Telegram tokens, WeChat bot IDs,
and Feishu app IDs are unique across agents. A duplicate returns 409 and names the
agent that owns the credential. QR login and registration sessions are also per agent.
Migration maps old settings, peers, and commands to `default`. Rollback keeps only
that agent's channel data. WeChat runtime remains keyed by bot ID and owner.

[`channel.Manager`](../../internal/channel/channel.go) owns bot lifetime,
commands, and reply delivery. Settings changes stop the old bot
before they start its replacement. Settings expose runtime status: `off`,
`running`, `error` with a reason, or `needs_login`.

`chat.Service.ReceivedSource` reports the global stored input fact. Channel
and platform adapters use it for duplicate input and attachment replay, including
when a bot credential moves between agents. `LookupSource` returns the
business-owned `SourceReceipt` for reply previews and upload cleanup. Its
references belong to the requested agent. A stored receipt means control
received the input, even when native admission is pending. Adapters do not infer
this state from `admitted_at` or terminal status. Native admission remains the
execution authority; reconciliation still retries pending input with its
stable request ID.

A peer names a remote destination, such as `telegram:7`. Owner input from all
channels enters the bot's agent `main` conversation with that peer as origin.
Channel input cannot select a Chat session. Old `channel_peers` records and
per-peer conversations remain stored but receive no new channel input. The
Sessions list excludes those conversations. Main and Chat are writable on
the web. Automation transcripts have no web composer.

`/new`, `/stop`, and `/help` use durable command
receipts. `/new` uses `channel:<source key>` as its stable reset request ID.
A crash after reset admission but before command completion reuses that reset.
A busy refusal is saved in the command receipt; replay cannot clear it later.
The command sends one short notice. Reset submissions have no model reply.
An incomplete command with an old peer channel key gets a notice with no action.
`/stop` records stop intent for main and
withdraws queued input and pending results. A channel Stop saves its answer
in the same transaction as the withdrawal. If command completion fails,
replay uses that answer without another withdrawal or native abort. The reply
confirms the cancellation request and states that processes and remote
work can continue. A stop error does not block later messages or other replies.

Each platform has one durable sender. Settlement wakes the origin's sender.
Main Stop wakes all senders for withdrawn input. At start
and after a failed send, it reads settled submissions and command replies
that remain undelivered, in submission admission order. It records each confirmed part and marks the reply
delivered after its last part. A permanent rejection skips that part and does
not block later replies. A transient error retries with a delay from 5 s to
60 s. Each bot receives a stable delivery ID and part index through `Bot.Send`.
Receipt reconciliation reads pending input and incomplete native abort intent.
Delivery is at least once: a crash between platform success and the local
commit can duplicate one part. Migration marks old settled replies as delivered
because earlier versions did not record delivery success.

See [ADR 0009](decisions/0009-channel-layer.md).

## Feishu and Lark

[`feishu.Bot`](../../internal/feishu/bot.go) receives private messages through
one WebSocket long connection. It needs no public callback URL. The REST
client caches the tenant token and refreshes an expired token. The transport
uses `coder/websocket` and a small protobuf Frame encoder and decoder. It
handles ping, pong, fragmented events, acknowledgement, and reconnect hints.
Startup failures report an error and retry with the REST backoff, up to
2 seconds between attempts. A configured bot retries until cancellation,
even if the server sends a finite `ReconnectCount`. After a disconnect, the
first reconnect waits only for the server's jitter. Later failures use the
server's reconnect interval. A socket with no data for two ping intervals
reconnects. A pong that changes the ping interval also resets the next ping.
The ping timer and read deadline use the same interval.
Fragment buffers expire after 5 seconds. One connection allows at most 64
fragmented events, 256 fragments per event, and 4 MB per event.

The Feishu channel page shows runtime status and supports QR app creation and
manual credentials. The form keeps the saved Feishu or Lark brand unless the
user changes it. QR setup requests `im:message`, `im:message.p2p_msg:readonly`, `im:resource`, and the
`im.message.receive_v1` event. The server keeps the device code in a session
until approval or expiry. Registration requests release the settings lock
while they wait for the accounts service. Cancellation or replacement rejects
late responses. Lark tenants use the Lark accounts service. Setup
saves the creator's `open_id` with the app credentials through
`settings.Service`. Manual setup requires a one-time code sent in a private
message. The code expires after 10 minutes. A credential change clears owner
authorization. The settings API never returns the saved app secret.

The bot ignores groups and other users. Text mentions are removed. Rich posts
become text and image attachments. Code blocks keep their text and language
in Markdown fences. Repeated `/pair` messages never reach the agent.
Message resources pass through `upload.Store`, with its 20 MB file limit and 10 attachments per message.
The shared channel manager handles `/new`, `/stop`, and `/help`. A source key contains `feishu:<app ID>:<message ID>`. The bot acknowledges valid agent
input after durable admission succeeds. Invalid content, rejected resource access,
and files with invalid names or excessive size are acknowledged and ignored.
Storage and temporary platform failures get a 500 acknowledgement for retry.

A preview card reads the chat's native watch events. It merges text updates
at 250 ms intervals and omits thinking and tool data. The shared durable
sender patches that card with the final answer, or creates a final card when
no preview exists. It uses the submission ID and part index for stable send
UUIDs. Each text part is at most 4000 UTF-8 bytes. A card rejected with HTTP
400 or an HTTP success status and code `230001` falls back to plain text.
Temporary or uncertain send
failures keep the card format and stable identity for retry. An uncertain final
patch keeps the preview ID, so durable delivery retries the same card. A successful
final patch restores `running` status.
Network failures, server errors and rate limits get at most
3 attempts per token. JSON calls and downloads share authorization and one
token refresh. A token rejection can therefore cause up to 6 API attempts,
plus token requests. Downloads wait at most 30 seconds for response headers;
each complete download has a 5-minute deadline, including error body reads.
Downloads use the socket session context.
Durable admission uses the bot lifetime, so a disconnect cannot cancel a stored
message while it waits for queue space. JSON responses remain
bounded to 4 MB and download error responses to 1 MB. Retry delays are
200 ms and 800 ms; `Retry-After`
can extend a delay to at most 2 seconds. Reply delivery remains at least once.
Previews use the bot lifetime and continue across WebSocket reconnects.
Previews are best effort and do not mark a reply as delivered. A restart can
leave a preview card in the platform; the durable sender sends a final card.

The standard `mise run check` task includes local platform tests and native HTTP
E2E tests for Feishu. These tests make no real platform requests.

## WeChat

[`wechat.Bot`](../../internal/wechat/bot.go) connects one iLink bot to its
owner's private messages. The WeChat channel page starts QR login and polls each
state. It asks for a phone verification code when required. A region redirect
changes the poll host. An expired or blocked QR code gets a replacement, with
at most three replacements per login attempt. The backend queries QR status only
when the page requests a poll. Closing the page stops new polls; an in-flight
request ends within 35 s. There is no fixed deadline for the whole login attempt.

Only a confirmed result with a bot token, bot ID, base URL, and owner ID can
activate the bot. Invalid confirmation stays failed on later polls. Valid
candidate credentials remain available if local activation fails. Activation
stops and joins the old bot before it changes shared runtime state. It saves
credentials and runtime initialization in one transaction, starts the replacement,
and then reports `confirmed`. If the transaction fails, it restores the previous
bot. A new login ID or delete invalidates an older login request.

The bot ignores other senders, group messages, bot echoes, and unfinished
messages. It stores the owner's latest context token before it admits a
submission. Each source key uses the bot ID and message ID. The bot advances
the durable update cursor only after every message in the batch is stored or
explicitly rejected. A repeated batch therefore cannot run a submission twice.
Cursor, context token, and pause time are runtime state in `control.sqlite`.
The settings API does not return them. Login initializes the runtime row; bot
reads use SELECT. A missing row is a storage invariant error and retries with
an error status.

Message IDs retain the full protocol `uint64` range. Context watermarks use
20-digit decimal TEXT so SQLite can compare them without signed truncation.
The context guard retains the existing assumption that larger message IDs are
newer. The [official Tencent client 2.4.10](https://registry.npmjs.org/@tencent-weixin/openclaw-weixin/-/openclaw-weixin-2.4.10.tgz)
declares `uint64` IDs but provides no
numeric chronology guarantee in its protocol types or receive loop. This
assumption remains unverified; no new ordering mechanism is introduced.

Text and voice transcription become message text. Quoted text gets a
`[引用: ...]` prefix. A video becomes a placeholder. Images and files pass
through CDN download and AES-128-ECB decryption into `upload.Store`, with the
same limits as web uploads: 10 files per message and 20 MB per file. Invalid
media is rejected. CDN HTTP 408 and 429 are temporary failures. A temporary
download or storage error keeps the batch cursor unchanged. Existing submission
and command receipts are checked before attachment downloads. An interrupted
command still completes through the shared manager. A failed or rejected attempt
removes its new uploads before admission. Admitted submissions keep their files.

While a submission is pending, the bot sends typing status every 5 s. Final
answers keep supported Markdown and use parts of at most 2000 characters.
Unanswered submissions send a short error message. The durable sender reads
the saved context token before each send, including after a restart.

## Failure behavior

- **dex exits.** Native execution and business receipts remain in their
  separate SQLite stores. The next start registers interrupted tasks before
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

Agent tools (`read`, `write`, `edit`, `bash`) run on the host without a
sandbox, inside the selected project directory or the agent workspace. The HTTP server listens on
`127.0.0.1:8787` by default. On loopback without DEX_TOKEN it has no
authentication. On any other host, or with DEX_TOKEN, every `/api/` request
needs the access token ([ADR 0007](decisions/0007-network-access-token.md)).
A chat channel must allow only known user IDs: the settings refuse a
Telegram bot without allowed users. WeChat captures the owner ID from QR
confirmation and rejects a login without that ID. Other WeChat senders never
reach the agent. The API refuses cross-site writes
(`http.CrossOriginProtection`), so another web page in the user's browser
cannot send messages or change settings.

`control.sqlite` holds provider keys, channel tokens, and WeChat context tokens as
plain text. WeChat logs exclude credentials and remote error text. The
data directory is created with mode 0700. The API never returns a key or a
token.

Uploads: a file name with a path, a control character, or more than 255
bytes is refused. An upload ID must be a UUID in its normal form, so it
cannot leave the uploads directory. `GET /api/agents/{agentId}/uploads/{id}` sends PNG, JPEG,
GIF and WebP inline with their type, and every other file as
`application/octet-stream` with `Content-Disposition: attachment`. All
responses have `X-Content-Type-Options: nosniff`, so an uploaded HTML or SVG
file does not run in the dex origin.

An MCP server from an mcp.json file runs as the user, without a sandbox, and
gets the explicit agent environment plus its own `env`. mcp.json holds headers and
environment values as plain text; the API returns no command, URL, header or
environment value. A server that fails shows only a category of the error,
such as an HTTP status, in the API and in the dex log. To see the full
error, run the server by hand.

Every chat reads the memory, including Telegram chats of each allowed user.
A memory entry can carry a prompt injection from a web page or a file to
later chats; the rules tell the agent that memory is data, not instructions,
and git history shows each change.

Control does not log the Telegram token; Bot API errors drop the request URL,
which holds the token. Tool and stdio MCP processes receive an explicit base
environment plus home, dex, and agent `.env` values. A server's own `env` wins.
The engine does not change the dex process environment. Tool execution does not
inherit ambient process credentials. Extension files remain outside the
sandbox boundary; the model can read them through tools.

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
state. Business services own receipts, channel reply delivery, tasks, and
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
