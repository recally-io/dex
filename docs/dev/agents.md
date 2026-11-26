# Agents

This document describes agents and the HTTP API, settings, memory, and the bot
tools.

## Agents and HTTP API

One owner can create several agents. `GET /api/agents` returns
`{agents: [Agent]}` in creation order. Each list entry has `activity` with
`activeSessions`, `runningChannels`, and `enabledAutomations`. One SQL query
counts sessions and automations across all agents. A child session counts when it has pending input, like any other visible session. Each session
counts once. Channel counts come from the app registry. The web polls this
list every three seconds and shares its cache across agent pages. The same
response includes per-agent `automationFailed`.
These fields come from the activity SQL query. `POST /api/agents` accepts a name
and optional primary model, thinking level, fallback model, instructions, and
Runner (`runnerId`). Omitted fields copy the built-in `default` agent; a model
that is no longer available is not copied. Empty strings choose no model or the
Local Runner. `POST /api/agents` returns the created agent with a UUID v7
ID (`201`). The ID is read-only. `GET` and `PUT` on `/api/agents/{agentId}` read
and save one agent. Names and models use the same validation for creation and
updates. A fallback model needs a primary model. `agentModels` wraps the runtime model catalog for each agent: if an
agent's primary model is missing or returns an error before any response block
streams (excluding aborts and context-overflow errors), `agentModels` switches to
the agent's fallback model.

Agent resources are under `/api/agents/{agentId}`: main, sessions and their
settings, messages, clear, history, abort and events; turn lookup; uploads;
integrations and reload; automations and their run and runs endpoints. An unknown
agent or a resource owned by another agent returns `404`. Upload storage uses
`<data dir>/agents/<agentId>/`. Control builds the path store for each request.
Each live stream uses `engine.Ref{AgentID, ID}` for watches and history.
Execution IDs can be equal in different agents.

An agent can be archived. `agents.archived_at` holds the time. The
filter is in SQL, not in the `enabled` flags: `ListAgents`, startup channel
start, dreams, `ListDueAutomations`, the activity query and Board attention all skip an
archived agent, so a restore needs no state to rebuild. `GET /api/archived-agents`
lists archived agents. `DELETE /api/agents/{agentId}/archive` restores. There is
no archive route: only an approved `bot_delete` question archives (see
[Bot tools](#bot-tools)). Every other agent route returns `404` for an archived
agent. The archive guard is one `UPDATE ... WHERE` in `internal/db`: the agent
exists, is not `default`, is not archived, and has no pending input in any
conversation, which covers queued inputs, Automation runs, and Dreams. Every input
writer (user and channel input, a context reset, an automation run, a child session
turn and a child result) calls `db.RequireActive` after it takes the SQLite
writer, in the transaction that stores the input. Input first makes the archive
fail as busy. Archive first makes the input fail with `ErrAgentArchived`, so an
archived bot never has a runnable input. After the commit,
`agent.Service.StopChannels` stops the Telegram, Feishu and WeChat clients under
its lock, and drops the unfinished WeChat login and Feishu registration. Every
channel activation (WeChat login, Feishu save, Telegram save) calls
`db.RequireActive` under the same lock before it saves credentials or starts a
client, so a poll that was already running cannot restart a channel.
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
with the saved agent ID. See [Skills and Integrations](skills-and-integrations.md) for the reload lifecycle.

The web UI uses the nested agent routes, including live events and upload
links. Agent resources require an explicit ID, including the default agent.
`GET /api/settings` returns global providers, built-ins, and APIs. `/settings/general` edits the `default` agent
through `PUT /api/agents/default`; there is no separate defaults table. Channel
setup and status use `/api/agents/{agentId}/channels`, with `/telegram`,
`/wechat`, and `/feishu` for changes.

## Settings

The web UI edits all settings.
[`settings.Service`](../../internal/settings/settings.go) owns providers, model
checks, and the access token. [`agent.Service`](../../internal/agent/agent.go)
owns agent settings, activity, and channel configuration. `session.Service` saves
session settings with an injected validator. Each change stays in `control.sqlite`:

| Setting                                 | Table                                               | Applied by                                     |
| --------------------------------------- | --------------------------------------------------- | ---------------------------------------------- |
| Bot model, thinking level, instructions | `agents`                                            | the next `submit` of each session              |
| Session model, session thinking level   | `sessions`                                          | the next `submit` of that session              |
| Providers                               | `providers`                                         | the engine catalog, and again at process start |
| Telegram credential, allowed users      | `telegram_settings`; managed token in private vault | a restart of the bot                           |

`PUT /api/agents/{agentId}` saves the name with the other bot settings.
The API trims spaces, then accepts 1 to 40 Unicode characters. The web uses the saved name in
the sidebar, the timeline header, and the composer.

Sessions default to the Bot's model and thinking level. A saved Session
override applies at the next admission, including channel input on Main. The
Main Web page has no Session model control. The API accepts settings on
writable Sessions and Automation transcripts. An Automation that reuses its
Session uses those settings. The thinking levels and
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

For session tools, custom `openai-responses` providers send `strict:false`.
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

## Memory

The memory is a git repo in the
[Agent Memory Repo](https://github.com/AgentMemoryRepo/agentmemoryrepo) format
at `<data dir>/memory/`. It belongs to the user, so all agents and channels
share it.

- **Read.** The [`memory` extension](../../internal/engine/memory.go) adds three
  system prompt sections: the memory rules (the repo path, the entry format,
  and the session's `source`), shared `MEMORY.md`, and that agent's
  `agents/<agentId>/MEMORY.md`. Other agents do not load this last section.
  They render before each model
  request, so a change applies from the next request, also inside a run.
  PiG sends a section again only when its text changed. The agent
  finds the other files with grep and the `[[path]]` links.
- **Write.** The agent changes the files with its edit and write tools,
  without approval. It does not run git. When a submission settles, the
  engine commits all changes as `Update memory from <source>`. The source is
  `dex:<agentId>/chat/<sessionId>`, or `dex:<agentId>/dream/<dreamId>`.
  Owner facts go in shared `MEMORY.md` and its topic files. Role experience
  and ways of working go in `agents/<agentId>/`, with its own `MEMORY.md`
  entry point. This keeps one bot's habits out of the owner's preferences.
- **Dream.** The [scheduler](../../internal/scheduler/scheduler.go) runs [`dream.Dreamer`](../../internal/dream/dream.go)
  at start and then every hour. The schedule only causes a check; the
  `dreams` table decides. One Dreamer checks every agent. When an agent has a
  model, none of that agent's submissions are pending, no dream is pending
  across all agents,
  the last dream started 24 h ago or more, and the oldest settled message that
  no dream read is 24 h old or more, the check starts a dream. A dream is a
  hidden session (channel key `dream:<id>`). Its system prompt holds the dream
  instructions and the privacy and language parts of the agent instructions.
  Its message holds the new messages as JSON lines, in settle order, with
  local times: up to 500 messages or 100,000 bytes. A text longer than 4,000
  characters keeps its first 3,000 and last 1,000 characters and is marked as
  cut. The agent adds what repeats across its sessions. It tidies its own folder
  and shared entries whose session sources all belong to it. Entries with other
  agents' sources, including mixed sources, can only gain `uncertain: yes`.
  Shared entries with no session source stay unchanged. The
  `dreams` table records the last message that each dream read; the next dream
  starts after that agent's newest done dream.

## Bot tools

`bot_list` reports `runningSessions` from `AgentRunningSessions`: the sessions
of the bot with a pending input, Main included. It is the busy rule of the archive
guard, so a count above zero means `bot_delete` refuses the bot. The sidebar's
`activeSessions` is a different count and leaves Main out.

Main and top-level Web sessions have `bot_list` and `bot_get`, and the three change
tools `bot_create`, `bot_update` and `bot_delete`. An Automation conversation
has only the read tools. The app selects the `bots` and `bot-manage` extensions
at admission. A child Session or Dream does not get the change tools. Tool
visibility does not grant authorization. `internal/agent.BotTools` maps each call to the same service methods as the
web (`CreateAgent`, `PatchAgent`). `session.Service.ToolBotCaller` checks the caller
again in control: the conversation is Main or a Session, and the source input is a
`message` with a user origin in that conversation. A user origin is an allow-list:
`web`, `telegram:`, `feishu:` and `wechat:`. `bot_create` and `bot_update` replay
as unsafe, because they have no receipt. A tool that PiG interrupted returns an
interruption error, and the model reads the list again.

`bot_delete` does not archive. It saves a question that carries an action
(`questions.action`, `action_agent_id`, `approve_text`) and ends the turn, as
`ask_user` does. It must be the only call in its round. Control archives inside
the receipt transaction of the answer, before the model sees the answer. The
transaction takes the answer text and origin, and archives only when the origin
is a user origin and the text equals `approve_text` after `session.NormalizeText`
(trim surrounding whitespace). The HTTP handler and this check use the same
function, so a channel that does not trim gets the same result as the web. It runs the archive guard
again, because the bot can become busy after the question. It stores the
decision in `questions.outcome` and appends it to the answer content, so the
model reads `dex: Approved. Bot "X" is archived.`, `dex: Not approved. ...` or
`dex: Approved, but bot "X" is not archived: <reason>.`. The channel stop runs
after the commit through `Service.OnBotArchived`, so a slow channel cannot hold
the admission gate. A decided action question cannot be withdrawn or reopened.
The archived bot's workspace, `state.sqlite`, memory and sessions stay on
disk. [Cross-bot sessions](sessions.md#cross-bot-sessions) describes cross-bot child
sessions, read, and search.
