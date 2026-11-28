# Bots

This document describes bots and the HTTP API, settings, memory, and the bot
tools.

## Bots and HTTP API

One owner can create several bots. `GET /api/bots` returns
`{bots: [Bot]}` in creation order. Each list entry has `activity` with
`activeSessions`, `runningChannels`, and `enabledAutomations`. One SQL query
counts sessions and automations across all bots. A child session counts when it has pending input, like any other visible session. Each session
counts once. Channel counts come from the app registry. The web polls this
list every three seconds and shares its cache across bot pages. The same
response includes per-bot `automationFailed`.
These fields come from the activity SQL query. `POST /api/bots` accepts a name
and optional primary model, thinking level, fallback model, instructions, and
Runner (`runnerId`). If omitted, the model choices, thinking level, and
Runner use the built-in `default` Bot's settings. Dex skips unavailable models.
Instructions do not inherit. Empty strings choose no model or the
Local Runner. `POST /api/bots` returns the created bot with a UUID v7
ID (`201`). The ID is read-only. `GET` and `PUT` on `/api/bots/{botId}` read
and save one bot. Names and models use the same validation for creation and
updates. A fallback model needs a primary model. `botModels` wraps the runtime model catalog for each bot: if a
bot's primary model is missing or returns an error before any response block
streams (excluding aborts and context-overflow errors), `botModels` switches to
the bot's fallback model.

Bot resources require an explicit Bot ID, including `default`. An unknown
Bot or a resource owned by another Bot returns `404`. See the
[HTTP contract](../../api/openapi.yaml) for routes. Execution IDs can match
across Bots, so live streams use `engine.Ref{BotID, ID}`.

A bot can be archived. `bots.archived_at` holds the time. The
filter is in SQL, not in the `enabled` flags: `ListBots`, startup channel
start, dreams, `ListDueAutomations`, the activity query and Board attention all skip an
archived bot, so a restore needs no state to rebuild. `GET /api/archived-bots`
lists archived bots. `DELETE /api/bots/{botId}/archive` restores. There is
no archive route: only an approved `bot_delete` question archives (see
[Bot tools](#bot-tools)). Every other bot route returns `404` for an archived
bot. The archive guard is one `UPDATE ... WHERE` in `internal/db`: the bot
exists, is not `default`, is not archived, and has no pending input in any
conversation, which covers queued inputs, Automation runs, and Dreams. Every input
writer (user and channel input, a context reset, an automation run, a child session
turn and a child result) calls `db.RequireActive` after it takes the SQLite
writer, in the transaction that stores the input. Input first makes the archive
fail as busy. Archive first makes the input fail with `ErrBotArchived`, so an
archived bot never has a runnable input. After the commit,
`bot.Service.StopChannels` stops the Telegram, Feishu and WeChat clients under
its lock, and drops the unfinished WeChat login and Feishu registration. Every
channel activation (WeChat login, Feishu save, Telegram save) calls
`db.RequireActive` under the same lock before it saves credentials or starts a
client, so a poll that was already running cannot restart a channel.
Session, health, maintenance, models, provider settings, and
`/api/automations/preview` stay global. Separate services own global settings
and bot state. The settings lock orders provider saves and local engine
configuration. Each settings request releases that lock before it waits for
its own discovery operation. The bot lock orders bot writes,
credential ownership checks, channel changes, and setup sessions across all
bots. Bot model checks distinguish pending discovery from an invalid model.
The lock order is bot, then settings. Settings never takes the bot
lock. Each service writes separate rows. Creating or selecting a bot does
not configure providers.
One scheduler job checks automations across all bots and posts each prompt
with the saved bot ID. See [Skills and Integrations](skills-and-integrations.md) for the reload lifecycle.

`GET /api/settings` returns global providers, built-ins, and APIs.
**Settings → General** edits the `default` Bot with `PUT /api/bots/default`.
See [Channels](channels.md) for pairing and runtime status.

## Settings

The web UI edits all settings.
[`settings.Service`](../../internal/settings/settings.go) owns providers, model
checks, and the access token. [`bot.Service`](../../internal/bot/bot.go)
owns bot settings, activity, and channel configuration. `session.Service` saves
session settings with an injected validator. Each change stays in `control.sqlite`:

| Setting                                 | Table                                               | Applied by                                     |
| --------------------------------------- | --------------------------------------------------- | ---------------------------------------------- |
| Bot model, thinking level, instructions | `bots`                                              | the next `submit` of each session              |
| Session model, session thinking level   | `sessions`                                          | the next `submit` of that session              |
| Providers                               | `providers`                                         | the engine catalog, and again at process start |
| Telegram credential, allowed users      | `telegram_settings`; managed token in private vault | a restart of the bot                           |

`PUT /api/bots/{botId}` saves the name with the other bot settings.
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
a failed discovery. The bot has no model until the user chooses one. New message
admission requires a selected model. Receipt
recovery first looks up the native request ID, so clearing or changing the
current model cannot replace an existing native answer.

## Memory

The memory is a git repo in the
[Agent Memory Repo](https://github.com/AgentMemoryRepo/agentmemoryrepo) format
at `<data dir>/memory/`. It belongs to the user, so all bots and channels
share it.

- **Read.** The [`memory` extension](../../internal/engine/memory.go) adds three
  system prompt sections: the memory rules (the repo path, the entry format,
  and the session's `source`), shared `MEMORY.md`, and that bot's
  `agents/<botId>/MEMORY.md`. Other bots do not load this last section.
  They render before each model
  request, so a change applies from the next request, also inside a run.
  PiG sends a section again only when its text changed. The Bot uses
  `memory_search` and follows `[[path]]` links with `memory_read`.
- **Write.** The Bot uses `memory_write` and `memory_edit` on the control host.
  When input settles, the engine commits edits as `Update memory from <source>`.
  Concurrent edits can share the first settling Session's commit. On open,
  the engine commits edits left by a crash. Dex does not synchronize the repo.
  The source is
  `dex:<agentId>/chat/<sessionId>`, or `dex:<agentId>/dream/<dreamId>`.
  Owner facts go in shared `MEMORY.md` and its topic files. Role experience
  and ways of working go in `agents/<botId>/`, with its own `MEMORY.md`
  entry point. This keeps one bot's habits out of the owner's preferences.
- **Dream.** The [scheduler](../../internal/scheduler/scheduler.go) runs [`dream.Dreamer`](../../internal/dream/dream.go)
  at start and then every hour. The schedule only causes a check; the
  `dreams` table decides. One Dreamer checks every bot. When a bot has a
  model, none of that bot's submissions are pending, no dream is pending
  across all bots,
  the last dream started 24 h ago or more, and the oldest settled message that
  no dream read is 24 h old or more, the check starts a dream. A dream is a
  hidden session (channel key `dream:<id>`). Its system prompt holds the dream
  instructions and the privacy and language parts of the bot instructions.
  Its message holds the new messages as JSON lines, in settle order, with
  local times: up to 500 messages or 100,000 bytes. A text longer than 4,000
  characters keeps its first 3,000 and last 1,000 characters and is marked as
  cut. The bot adds what repeats across its sessions. It tidies its own folder
  and shared entries whose session sources all belong to it. Entries with other
  bots' sources, including mixed sources, can only gain `uncertain: yes`.
  Shared entries with no session source stay unchanged. The
  `dreams` table records the last message that each dream read; the next dream
  starts after that Bot's newest done dream. A failed or interrupted Dream
  leaves that cursor unchanged for the next eligible check. `memory_source_read`
  can read the original text of a cut record selected for the current Dream.

## Bot tools

`bot_list` reports `runningSessions` from `BotRunningSessions`: the sessions
of the bot with a pending input, Main included. It is the busy rule of the archive
guard, so a count above zero means `bot_delete` refuses the bot. The sidebar's
`activeSessions` is a different count and leaves Main out.

Main and top-level Web sessions have `bot_list` and `bot_get`, and the three change
tools `bot_create`, `bot_update` and `bot_delete`. An Automation conversation
has only the read tools. The app selects the `bots` and `bot-manage` extensions at admission.
A child Session or Dream does not get the change tools. Tool
visibility does not grant authorization. `internal/bot.Tools` maps each call to the same service methods as the
web (`CreateBot`, `PatchBot`). `session.Service.ToolBotCaller` checks the caller
again in control: the conversation is Main or a Session, and the source input is a
`message` with a user origin in that conversation. A user origin is an allow-list:
`web`, `telegram:`, `feishu:` and `wechat:`. `bot_create` and `bot_update` replay
as unsafe, because they have no receipt. A tool that PiG interrupted returns an
interruption error, and the model reads the list again.

`bot_delete` does not archive. It saves a question that carries an action
(`questions.action`, `action_bot_id`, `approve_text`) and ends the turn, as
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
