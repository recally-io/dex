# Sessions

This document describes conversations, Main, Chats, child sessions, and
internal delegation.

## Conversations

Control stores the parent of a child session in `conversations.parent_id` and the
turn that created work for it in `submissions.parent_request_id`. A child session is a
Chat that a parent session created. It keeps bash and all coding tools, and the user
can write to it at any time. The engine owns its transcript and execution records.
Business services do not query execution SQLite. There is no user goal lifecycle
or automatic retry chain.

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
It defaults to the bot's model settings and uses its instructions and shared memory.
It does not read
main's model context or send its answers to main.

`GET /api/agents/{agentId}/chats` lists session summaries
by `updatedAt` descending, then ID descending. `updatedAt` is the latest
conversation creation, or input creation or settlement. List and detail use the
same rule. The list excludes main, old channel conversations, dreams and
Chat sessions with no message. A child session is a row with `parentId`; a task
from before child sessions has the same shape. While a session is busy, the list
adds `lastActivityAt` and `toolStartedAt` from the engine. The web uses them to
show a stalled hint. Detail does not carry them.

The list of a bot also holds the children of its sessions that belong to another
bot. Such a child has `agentId` and `agentName`, and its row opens under that bot.
A child row names the bot of its parent in `parentAgentId` and `parentAgentName`
when the parent belongs to another bot than the one in the path. Detail has the
parent fields.

`GET /api/agents/{agentId}/turns/{turnId}` returns the session that holds a
turn. Result links and old `/tasks/<id>` links name a turn, because the ID of an
old task is the ID of its first input. The route returns `404` for an unknown
turn and for the turn of another agent, except a turn of a child of a session of
this agent in another bot. That child names its bot in `agentId`, and the web
opens it under that bot.

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
Automation, parent turns to a child session, and dream input use `automation`, `task`, and `dream`.
A child result copies the origin of the parent request in the same transaction
that settles the child turn. A result chain thus keeps the
first input's origin. Existing rows use `unknown`. The durable channel sender
routes ordinary replies and task results by origin. Web input sends no channel reply.
The engine stores origin with native input metadata. One projection exposes it
in queued input, snapshots, live events, and history pages. The web shows the
channel name beside each channel input.

The first input owns a run. Its origin selects the reply recipient, and its
receipt is the source for tool calls. A steered input adds content only. It does
not change the recipient or grant or remove tool authority. Web input and a
parent's `session_send` can steer. Channel, automation and dream inputs always
use `followUp`. Bot and routine management check the source input, not the
steered input.

## Internal delegation

`ask_user` is installed only on Main and Chat conversations. Extension edits
persist per conversation, so tasks, automations and dreams never get it. A
`questions` row holds the text, the options and one of `open`, `answered`,
`consumed` and `dismissed`. A partial unique index allows one open question per
conversation. The tool key is the agent ID plus PiG's tool task ID, so a replay
returns the saved question. The tool reads `harness.LiveState` and returns
`ToolControl{Terminate: true}` only when it is the only call in the round.
PiG ends the turn only when every call terminates, so a mixed round gets a tool
error and no question. The answer is a normal message with `answersQuestionId`.
The receipt, the new `answered` status and the content wrapper `Answer to:
<question>` commit in one transaction. A message without that field never closes
a question. Withdrawing the answer reopens the question. The answered input's
settlement consumes it. Legacy Stop, Clear and archive dismiss open and answered
questions in their own writer transaction. Settlement of the asking input adds
the question to the reply, so a channel receives it through the reply outbox.
`Bot.Send` returns the remote message ID. After each confirmed part, the sender
stores the ID in `question_messages` for the question. Its key contains the
agent ID, peer, and remote message ID, because different bots can return equal
message IDs for the same peer. Telegram reads
`reply_to_message.message_id` and Feishu reads `parent_id` as `Message.ReplyTo`.
`SubmitMessage` maps a quoted ID to `answersQuestionId`. A quote of any part
answers the question. A quote of a closed question, an unknown ID or a platform
without quoting (WeChat) is a plain message. A crash between the remote send and
the mapping write loses the mapping of that part, so its quote is a plain message.
The settle worker takes the Session delivery gate before it settles an aborted
input. A withdraw holds the same lock, so a withdrawn channel message never gets
a "no answer" reply.

Main is a front desk. A person waits for each main reply. A channel receives
the reply only when the turn ends, and later input waits for that turn. So main
answers short requests directly. It sends work that needs many tool calls, long
commands, code changes or research to a child session with `session_new`. It
then ends the turn with a short reply. When the user writes while a child runs,
main answers first and stops or steers a child whose goal changed.

Each Chat and automation session runs one main agent loop. The loop does
ordinary work itself. It uses child sessions only for independent parallel work,
long commands whose output would flood its context, and independent review.

A child session is a Chat with `conversations.parent_id`. It has its own context
and runs at once. There is no count limit and no cwd lock. Children in one
directory run at the same time, so the prompt tells the parent to give each
writing child separate files or directories. The tools are:

- `session_new` creates a child and sends its first message. With `bot` (an ID
  or a name) the child belongs to another bot. See "Cross-bot sessions" below.
- `session_send` sends a message. An idle child starts a run. For a busy child,
  mode `steer` joins the current run after its current tool step, and mode
  `queue` (the default) runs after it.
- `session_stop` withdraws queued input and stops the run. It is the same as
  user Stop and has no result.
- `session_fork` branches a child up to a finished answer, with the same parent.
- `session_list` lists the children of the caller, whatever their bot, and with
  scope `all` also the sessions of one bot, except dreams. `bot` selects those
  other sessions: empty (the bot of the caller), `all`, or a bot ID or name. It
  never hides a child of the caller. Each row names its bot.
- `session_read` returns turns newest first: the input and the final answer of
  each turn, cut at 4,000 characters. A running session has `activity.available`.
  When available, `activity.runningTools` lists safe tool names and the time each
  was first observed running, in Unix milliseconds. It includes no tool arguments,
  output, or thinking. Other tool names become `other`. An available empty list
  means no tool was running in the last observation. Unavailable means there is
  no live observation. The engine observes child sessions only, and a watch can
  fail or end before settlement. It reads any session of any bot except dreams.
  `turn` with `offset` reads the rest of a cut text.
- `session_search` finds turns whose input or answer contains every word of a
  query. It searches the current session by default, including turns before
  compaction. It skips the turn that calls the tool and excludes dreams. Pass a
  numeric session ID to search one specific session, or `session: "all"` to
  search all sessions in the selected bot scope. `bot` defaults to the caller's
  bot and also accepts `all`, a bot ID, or a bot name.

Each session, list row and match names its bot (`bot` and `botId`).

Only a parent can send to, stop or fork a child. A child has no session tools,
no routine tools and no `ask_user`: the engine removes them from every input of
a conversation that has a parent, and the handlers refuse the call as well.
A conversation that was a task before the upgrade is a child in the same way.

## Cross-bot sessions

A child can belong to another
bot. Control keeps these rules, and a reviewer must check each new cross-bot path
against them:

- Ownership is `child.parent_id = caller conversation`. It does not depend on the
  bot of the child. `childOf` is the only check for `session_send`,
  `session_stop` and `session_fork`. A conversation that is not the parent, in
  any bot, gets "not found".
- `session_new` with `bot` creates the conversation under the target bot. The
  child takes the model, thinking level and workspace defaults of that bot, not
  the settings of the parent. Admission reads the agent of the child, so the
  child runs with the instructions, memory section, skills, MCP servers, and
  vault of that bot. A child still cannot create children.
- A result goes to `conversations.parent_id` in the settlement transaction, in the
  bot of the parent. The parent Session takes it from the same receipt queue as its other inputs.
- No call path holds two Session delivery gates. A session tool runs in its own
  conversation without the delivery gate. `session_stop` takes only the target
  child's gate. A cascading Stop records the targets in a short control
  transaction. It does not call native execution under the writer. Each target
  processes its cancellation under its own gate, across bots as necessary.
  For a native abort, settlement waits for the target's gate so Withdraw can
  finish its receipt first. The transaction that stores a result does not take
  the parent's gate.
- Restart recovery settles the admitted inputs of each child under the agent of
  the child, and sends the one `interrupted` result to the parent in its bot.
- `session_read` reads any session except dreams. With `session: "all"`,
  `session_search` searches all sessions of the selected bot or bots.
  `session_list` always shows the children of the caller and takes `bot` for the
  other sessions. The default bot is the caller's bot. An ambiguous bot name is
  an error that lists the IDs.
- A child delivers files to its own bot's artifacts directory. Its tool result
  provides that bot's file URL. A parent can link to the URL, but its bot's
  directory does not contain or list the child's files.

A tool call uses its bot ID and PiG's tool task ID as the turn ID. The turn
is stored with `submissions.parent_request_id` in the same write transaction as
the child. A replay finds the stored turn and changes nothing. Stop and the
tool calls take the SQLite writer before they read.

A turn that a parent sends has origin `task`, so it never goes to a user
channel. A completed run with a parent input sends one `task_result`, including
when a user started the run. A pure user run sends none. Control uses the child
conversation and native Answer entry as a unique key. The first parent input
to settle owns `result:<turn ID>`. A done native input without an Answer is an
error. Each failed or interrupted parent input sends its own result; user inputs
send none. A stopped, aborted or withdrawn input sends none. Result insertion
and receipt settlement share a control transaction. Only an Answer key conflict
is treated as already delivered.

The result names the child, its title, the turn and the outcome. It has the
final answer cut at 4,000 characters, or the failure reason. A restart adds
the number of queued inputs it withdrew. The result copies the origin of the
parent request, so a Chat gets its own results and a channel peer gets the
results of work that the peer started.
The result enters the parent's follow-up inbox even when the parent is busy.
Control never changes a settled submission back to pending, so there is no
retry counter and no delivery recovery state. The parent reads the child's
final answer from the result and can call `session_read` for a cut text.

Control passes a trusted input kind, the turn ID and the child title to the
engine. The engine stores results as `dex.task_result` entries. The public
projection replaces each result input with a `task` mark. The mark holds the
title, the outcome and the failure reason, and never the answer text. A failed
result reply cannot retry the user message before the mark.
Neither layer uses message text or an ID pattern to hide results.

The web shows a child as a thread. The main
transcript keeps one card at each `session_new` or `session_fork` call and one
line at each `session_send` call. A result mark is one line with a "View thread"
action. The card, the line and the action open a read-only side panel that
shows the child's own transcript with the usual chat reads and stream. The web
reads the child ID from the session tool result and finds the child of a result
mark with `getTurnChat`. Channels have no thread, so a channel user sees only
the reply of Main. Child
conversations stay absent from dream inputs, and dream input selects only
ordinary messages.

A card shows the child's current state, not the outcome of the dispatch that
created it: waiting, running, idle, or unavailable. Idle does not mean success;
result marks keep their recorded outcomes. The shared child activity watch
wakes subscribers when native running changes. It remains open for queued
inputs after the first input settles. Parent SSE subscribes to child IDs found
in its current native window and sends only changed current states. It checks
bot and parent ownership before it emits child metadata. It does not read all
past dispatches, submissions, or child transcripts. Cards outside that window
have unavailable status; opening the panel reads the child's own stream.
Disconnect makes status unavailable. A reconnect reads the current snapshot
and child states.

A child is writable on the web at any time. A user message goes to the child
like to any Chat and sends no result. On Main, the web composer uses Main's
live run state. It can send while a child runs. Web Main Stop requires the
exact foreground task target from the connected snapshot or run event. The
engine checks ownership and the current generation. PiG does not reuse task
IDs on handoff, reset, or restart, so replay cannot stop a successor.
Main keeps queued and awaiting inputs, children, background commands and
Monitors. PiG 0.7 task abort keeps the Inbox but does not start its next input.
Dex records the Stop intent and takes a native Continue boundary if the target
aborted, the Session is idle, and input is queued. Recovery can finish this step
after a restart.

Web Stop on other sessions and channel `/stop` keep legacy behavior. They
withdraw pending inputs and stop eligible work, including background commands,
Monitors and child work the parent started. A child with pending user input
keeps its run. Only its parent inputs that are still queued or not yet admitted
are eligible for withdrawal. Its own dispatcher checks placement before it
withdraws each exact request. `/clear` does not cancel children.

Admission, Clear, Withdraw and reconciliation use a delivery gate for each
Session. Legacy Stop requests stay in business storage until the target processes
them. Admission on that target reconciles the requests before later input. Cold
recovery and legacy full Stop save passive display entries for withdrawn native
inputs without adding model context. The shared SQLite writer runs short
transactions. The global receipt gate orders maintenance confirmation, as
described in [Service and updates](architecture.md#service-and-updates).
The reply of Stop confirms the request. It does not prove process exit.
Processes and remote work can continue. Cancellation does not undo file writes
or remote actions.

At process start, control reads the native state of every pending input in a
child with a pending parent input, including admission gaps. The engine reads
committed records without opening or resuming a Harness. Control never opens
`state.sqlite`. A saved completion or failure settles through the normal
transaction. If a parent input is placed in an unfinished run, recovery
interrupts the placed inputs and withdraws queued and unadmitted inputs. Each
placed parent input gets an interrupted result with the withdrawal count.
Settlements, results, withdrawals and abort intent commit together before any
Harness resumes. Recovery isolates those conversations. A second restart adds
nothing. The parent reports the interruption and warns that side effects may
exist.

The SDK owns cancellation of run invocations and tools. Control does not track
process trees or confirm exit. A normal local or MCP tool return can settle
execution. An aborted MCP call ends the local wait and requests remote
cancellation. It does not prove that the remote job stopped. Before another
turn, the parent must check artifacts and prior actions.

Tools run through [`internal/execenv`](../../internal/execenv) using the
built-in `local` provider, a remote `pi-env` SSH connection, an `e2b`
sandbox endpoint, or a machine service connection. The
[remote machine](../remote-machines.md) opens an outbound WebSocket to Dex
behind the same HTTPS front as the web UI, and uses the public PiG execution API
on the target. Enrollment trades a one-time code for a machine token; Dex stores
the SHA-256 digest of that token in `control.sqlite` and keeps live connections
in memory. A bot borrows the connection, so reloading the bot does not disconnect
its machine. Each backend owns its connections, and a `Handle` is one lease on
them: all bots on
one `pi-env` environment share one SSH connection until that environment changes. The socket carries JSON-RPC 2.0 calls from
[`sourcegraph/jsonrpc2`](https://github.com/sourcegraph/jsonrpc2): one call per
operation, one operation at a time, and a `cancel` notification for the call
that the user stopped. Dex waits for the canceled operation to finish before it
sends the next call. The library owns call correlation, reply routing, and
session loss, so Dex owns only the operations. A session does not replay calls
after connection loss. Dex reports a missing final result as unknown.
A lease on a machine holds its enrollment, which is the digest of the machine
token in control storage. It does not hold the socket, and the stamp does not
change when the machine disconnects or Dex restarts. Each call takes the current
session of the machine. If the machine is offline, the call waits up to 10
seconds for a session of the same enrollment before Dex sends it. A revoke during
the wait ends the call at once. If the machine reports another workspace or home
than the session that opened the lease, the call fails until a reload. If the session ends before Dex sends the call, Dex
tries once more on the next session. A call that Dex sent is never sent again.
When the machine connects again with the same token, the new session replaces
the earlier one at once, so a half-open socket does not block the reconnect.
Each new session wakes the chats that wait, so their input runs without a new
message. A revoke or a new enrollment changes the identity, so a lease fails at
once and the bot reloads.
`execenv.Manager` dispatches each kind to one backend and owns the environment
lifecycle (`Normalize`, `Probe`, `Changed`, `ForgetHostKey`) and the agent
lifecycle (`CanResetAgent`, `ResetAgent`, `ReleaseAgent`). This keeps
`internal/settings` independent of backend config schemas and SSH `known_hosts`
storage. `execenv.Runners` owns machine enrollment for the HTTP routes. SSH environments default `loginShell` to
`true`: they start the daemon via `$SHELL -lc` and capture interactive login
shell exports via `$SHELL -l -i -c '... env -0'` on open.
At startup, a runner uses a bounded interactive login shell probe on its own
host to get only the exported `PATH`. The probe passes through the service's
`ZDOTDIR` and `XDG_CONFIG_HOME` when set, so the shell can find its startup
files. It does not add those variables to the tool defaults. The probe uses
the same `/usr/bin/env -0` command as Local, without a `PATH` lookup. The
runner limits the probe output and keeps only the `PATH` entry. If the probe
fails, the runner logs the reason without profile output or values, and
tools keep the service `PATH`. Bash tool
commands still run in a non-login, non-interactive shell. An explicit command
`PATH` takes precedence; commands that disable environment inheritance do not
receive the login or service `PATH`.
`GET /api/settings/environments/ssh-config` parses `~/.ssh/config` (including
`Include` directives) and lists private key filenames without reading key bytes.
`POST /api/settings/environments/probe` validates and probes unsaved settings,
using a temporary `known_hosts` file for new SSH endpoints and reusing saved
trust entries when the host and port match an existing environment. When a remote
SSH host key rotates, probes report `hostKeyChanged`, and
`POST /api/settings/environments/{environmentId}/forget-host-key` clears the
saved entry. For `e2b`, each environment can select E2B Cloud or override
`apiUrl`, `domain`, `envdUrl` (header-routed or `{sandboxId}`/`{port}`
templates), and `autoPause`, with optional secrets on custom endpoints,
HTTP `*.localhost` loopback dialing, `/timeout` + `GET /sandboxes/{id}` fallback
when `/connect` is unsupported, and `uname -sm` platform detection. Memory tools
(`memory_search`, `memory_read`, `memory_write`, `memory_edit`) stay bound to
`<data dir>/memory/` on the control host. CLI tools must be
logged in and run in the foreground with non-interactive options. Long work
uses [command Monitors](execution.md#command-monitors), which dex tracks. Any other
process must not outlive its turn. A turn can start and supervise a process,
such as a worker in a Rex tab, but must stop it before the final answer. Bash has no default
timeout. Its visible output keeps at most the last 2000 lines or 50 KiB,
whichever limit comes first; complete output goes to a temporary file.
A durable checkpoint does not attach a CLI after restart or guarantee that
external actions occurred once.

## Session environments

Each unscoped session runs its command and file tools in one execution
environment. `conversations.environment_id` in `control.sqlite` is the source
of truth. Empty means that the session follows the bot's environment
(`agents.environment_id`). `local` or an environment ID is an explicit
selection, which does not change when the bot's environment changes. An unscoped
session can select local or any registered SSH, runner, or E2B environment in the
same Dex instance. Bots that use the same runner share its connection, not their
workspaces. They execute one operation at a time. SSH and runner
use the same `<base>/<bot-id>/workspace` layout under their respective base
directories. These workspaces are not permission boundaries.

The engine resolves the environment of a session at each call, through the
`SessionEnvironment` callback that chat provides. One resolver
([`internal/engine/session_env.go`](../../internal/engine/session_env.go))
serves the Harness `Env` hook, the `bash` tool, background commands, upload
staging, project paths, the project `AGENTS.md`, and artifact delivery. The open
agent keeps one lease for each environment that its sessions use, and closes the
leases on reload and close. A bot with an unavailable default can still run
a session that selects another environment. New input that follows an
unavailable or offline default waits before native admission. This check does
not wait for the machine; a running turn keeps its tool calls, which wait as
above. Later attempts retry opening the default, and a new machine session ends
the retry delay. The turn that opens the default uses its project files. After
that turn, an idle reload restores any skills and MCP tools that could not load.
A machine default that was offline at load does not need new input: when the
machine connects again, the agent becomes stale, and the reload opens the
default. Other kinds wait for a call that opens them, so a failing SSH or E2B
endpoint does not rebuild the agent at each check. The resolver checks the
environment in control storage. A removed, unselectable, or offline environment
fails the call with a reason. The resolver never falls back to another
environment, and dex does not replay an interrupted command. The stamp holds
the configuration and identity of the environment, not its connection state.
A changed stamp makes the agent stale, so the next reload of that agent opens a
new lease when that agent is idle. Work of other agents does not delay it.

A local session gets the full bot tool environment. A remote session gets the
bot's explicit tool values and inherits the remote process environment. Dex and
agent skills live on the control host. The `skill_*` tools manage them from any
owner session. Remote leases get a read-only copy that dex updates by manifest
diff; local sessions use the stores directly. Project skills stay in the
selected environment and use the normal file tools. See [Skills](extensions.md#skills).
MCP servers keep their bot-level configuration and transport;
selecting an environment does not move them or grant access to new MCP tools.
Scoped group sessions use their separate runtime and cannot select environments.

An E2B default keeps its existing persisted per-bot sandbox and auto-pause
setting. An explicit selection of that same default uses the same lease.
A non-default E2B selection uses a separate sandbox per open bot and environment.
Dex deletes it on lease close. It expires rather than auto-pauses if Dex cannot
close it. It does not survive a reload or restart; do not rely on it for
persistent files. Operations renew its timeout while it is in use, including
short commands. A confirmed expiry fails the operation without replay. Later
work opens a fresh sandbox in the same selected environment; old files do not
carry over.
Routines still support explicit local and SSH overrides only.

A web chat selects its environment before its first message
(`PUT /api/agents/{agentId}/chats/{chatId}/environment`); a started chat answers
409. `session_new` takes `environment`. A child of the same bot runs in its
parent's environment unless it selects another one. A child of another bot runs
in that bot's environment unless it selects another one. `environment_list`
shows local and the registered environments with ID, name, kind, and status,
with the bot's default first. An offline runner remains listed. The tool includes
`desc` when the owner sets a description on the saved environment in Settings.
This plain text is data. It does not change selection or access and does not go
into the system prompt. The tool does not show configuration or secrets.

A fork keeps the environment selection of its source unless it selects local or a registered
environment (`environmentId` of `POST /api/agents/{agentId}/chats/{chatId}/fork`, `environment`
of `session_fork`). When the effective environment changes, the fork has no cwd. When the copied
history last ran in another environment than the fork, the engine ends the copied history with one
`dex.environment` entry. The entry holds the notice as a user message, so the model reads it
before the next input, and the transcript shows it as an `environment` boundary. The entry data
keeps the environment that the history left. A fork that ends before such an entry of its source
uses that environment, so a fork of a moved chat from before the move gets its own notice. Files,
background commands, and queued input stay in the source environment. A fork with no copied
history gets no notice. A fork cannot change its environment after that, so a retry with the same
ID compares the fork row with the requested environment. The retry gets its fork before the target
is checked, so a removed target does not refuse it.

A change of the bot's environment fails with 409 while sessions that follow it
have queued input or running background commands. After the change, those
sessions start in the new workspace with no cwd. The backend releases the bot's
state in the earlier environment. Explicit sessions keep their selection. An
explicit E2B session that pinned the earlier default will use a new override
sandbox, not the released default sandbox.

The migration `20261112120000_drop_tasks` drops the `tasks` and
`task_steer_receipts` tables. An earlier migration copied the parent link and
the parent request of each old task into `conversations.parent_id` and
`submissions.parent_request_id`. Each old task conversation is a child session,
and the ID of an old task is the ID of its first input. Old results stay as
`task_result` inputs. The saved task report and the acknowledgement of a task
are gone, and so are the old task endpoints. A rollback of the migration
restores empty tables.
