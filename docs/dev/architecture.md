# Architecture

This document is for developers who change dex. It describes the subsystems,
their sources of truth, the message flow, and the failure behavior.

## Runtime overview

![Runtime boundaries and data owners](../../assets/architecture.png)

Source: [architecture.puml](../../assets/src/architecture.puml).

Control stores messages before delivery. The worker owns agent execution and
storage. The browser receives a transcript through control. Both processes
ship in one binary; they do not share database access.

## Processes

dex is one binary with two processes:

| Process | Code                                              | Responsibilities                                                                 | Does not                              |
| ------- | ------------------------------------------------- | -------------------------------------------------------------------------------- | ------------------------------------- |
| control | Go: `cmd/dex`, `internal/`                        | HTTP API and web UI, chat channels, timers, `control.sqlite`, the worker process | run agents or call models             |
| web UI  | React: `web/`, built into `web/dist` and embedded | chats, live transcript, settings pages, in the browser                           | hold state that the API does not have |
| worker  | TypeScript on Bun: `worker/`                      | one pi-durable Harness per agent, model calls, tool calls                        | know about channels or time           |

`mise run build:worker` compiles the worker with `bun build --compile` into one
executable (about 60 MB, Bun runtime included) and gzips it (about 30 MB) into
`internal/workerbin/dex-worker.gz`. The Go binary embeds that file. At start,
[`worker.Install`](../../internal/worker/install.go) writes it to
`<user cache dir>/dex/worker-<hash>` once, and control starts it as a child
process. See [ADR 0001](decisions/0001-embedded-bun-worker.md).

## Sources of truth

| Data                                 | Owner               | Location                                                                  |
| ------------------------------------ | ------------------- | ------------------------------------------------------------------------- |
| Agents, chats, submissions, settings | control             | `<data dir>/control.sqlite` ([migrations](../../internal/db/migrations/)) |
| Transcripts, runs, tool calls, usage | worker (pi-durable) | `<data dir>/agents/<id>/state.sqlite`                                     |
| Agent files                          | the agent's tools   | `<data dir>/agents/<id>/workspace/`                                       |
| Memory (git repo)                    | the agent's tools   | `<data dir>/memory/`                                                      |

The default data directory is `~/.dex` (`DEX_DATA_DIR`). pi-durable allows one
process per storage, so only the worker opens an agent directory.

## Memory

The memory is a git repo in the
[Agent Memory Repo](https://github.com/AgentMemoryRepo/agentmemoryrepo) format
at `<data dir>/memory/`. It belongs to the user, so all agents and channels
share it. See [ADR 0004](decisions/0004-agent-memory-repo.md).

- **Read.** The [`memory` extension](../../worker/src/memory.ts) adds two
  system prompt sections: the memory rules (the repo path, the entry format,
  and the chat's `source`), and `MEMORY.md`. They render before each model
  request, so a change applies from the next request, also inside a run.
  pi-durable sends a section again only when its text changed. The agent
  finds the other files with grep and the `[[path]]` links.
- **Write.** The agent changes the files with its edit and write tools,
  without approval. It does not run git. When a submission settles, the
  worker commits all changes as `Update memory from <source>`. The source is
  `dex:chat/<chat ID>`, or `dex:dream/<dream ID>`.
- **Dream.** The [scheduler](../../internal/scheduler/scheduler.go), a
  wrapper of gocron, runs [`dream.Dreamer`](../../internal/dream/dream.go)
  at start and then every hour. The schedule only causes a check; the
  `dreams` table decides. When the agent has a model, no message is pending,
  the last dream started 24 h ago or more, and the oldest settled message that
  no dream read is 24 h old or more, the check starts a dream. A dream is a
  hidden chat (channel key `dream:<id>`). Its system prompt holds the dream
  instructions and the privacy and language parts of the agent instructions.
  Its message holds the new messages as JSON lines, in settle order, with
  local times: up to 500 messages or 100,000 bytes. A text longer than 4,000
  characters keeps its first 3,000 and last 1,000 characters and is marked as
  cut. The agent adds what repeats across chats and cleans up the memory. The
  `dreams` table records the last message that each dream read; the next dream
  starts after the newest done dream.

## Automations

An automation runs a prompt on a schedule; see
[ADR 0005](decisions/0005-automations.md).
[`automation.Service`](../../internal/automation/automation.go) stores
automations and their runs in `control.sqlite`. The scheduler calls its
`Check` every minute. `Check` posts the prompt of each automation whose
`next_due_at` has come, as a message to its chat (`automation:<id>`, or
`automation:<id>:<run id>` for a new chat each run), and moves `next_due_at`
past now. The message uses the normal durable delivery.

## Settings

The web UI edits all settings; see [ADR 0002](decisions/0002-settings-in-control-db.md).
[`settings.Service`](../../internal/settings/settings.go) validates a change,
saves it in `control.sqlite`, and then applies it:

| Setting                       | Table               | Applied by                                                         |
| ----------------------------- | ------------------- | ------------------------------------------------------------------ |
| Agent model, instructions     | `agents`            | the next `submit` of each chat; the worker calls `configure` on it |
| Providers                     | `providers`         | `configure` to the worker, and again after each worker start       |
| Telegram token, allowed users | `telegram_settings` | a restart of the bot                                               |

The user adds any number of providers, and then chooses one model from all of
their models. A provider is one of:

- **Built-in**: a pi-ai provider (Anthropic, OpenAI, OpenRouter, and more) with
  an API key.
- **Custom**: an ID, a base URL, an optional API key, and one of the APIs
  `openai-completions`, `openai-responses`, or `anthropic-messages`. Its
  models are the listed model IDs, or else the ones that the endpoint's
  `/models` (OpenAI) or `/v1/models` (Anthropic) returns. A custom provider
  without a key sends the key `none`, for local servers.

The worker keeps the providers only in memory (built-in keys in pi-ai's
`InMemoryCredentialStore`) and gets no provider environment variables. When a
custom provider's model list fails, the provider stays saved and the API
returns the reason. When a change is saved but the
worker cannot apply it, the API returns an error; the next worker start
applies it. The agent has no model until the user chooses one; a message
before that is `unanswered`.

## Worker protocol

Control writes one JSON request per line to the worker's stdin. The worker
writes one JSON response or event per line to its stdout. Logs go to stderr.
The types are in [`worker/src/protocol.ts`](../../worker/src/protocol.ts) and
[`internal/worker/protocol.go`](../../internal/worker/protocol.go).

| Method               | Params                                                                                 | Result                    |
| -------------------- | -------------------------------------------------------------------------------------- | ------------------------- |
| `ping`               | none                                                                                   | `{}`                      |
| `createConversation` | `agentId`, `model`, `instructions`                                                     | `conversationId`          |
| `submit`             | `agentId`, `conversationId`, `model`, `instructions`, `requestId`, `content`, `source` | `submissionId`            |
| `configure`          | `providers`: `id`, `name`, `api`, `baseUrl`, `apiKey`, `models`                        | `errors` per provider     |
| `listProviders`      | none                                                                                   | built-in `providers`      |
| `listModels`         | none                                                                                   | `models` with credentials |
| `watch`              | `watchId`, `agentId`, `conversationId`                                                 | `{}` after the snapshot   |
| `unwatch`            | `watchId`                                                                              | `{}`                      |
| `abort`              | `agentId`, `conversationId`                                                            | `{}`                      |

The worker runs the submits of one conversation in order. When `model` or
`instructions` differ from the conversation's, it changes the conversation
first; a run in progress keeps its settings until its next turn.

Event `settled`: `requestId`, `status` (`done` or `unanswered`), `answer` or
`reason`. The worker sends it when a submission has its final answer.

Event `chat`: `watchId` and `events`, the chat events of one commit, or `end`
when the watch stopped. The first event of a watch is a snapshot. The worker
places each message as an entry of kind `dex.user` with control's submission
ID, so the user item in the transcript has the ID that the browser sent.

## Message flow

![Message flow](../../assets/message-flow.png)

Source: [message-flow.puml](../../assets/src/message-flow.puml)

1. The browser sends `POST /api/chats` (new chat) or
   `POST /api/chats/{id}/messages` with a UUID that it makes.
   [`chat.Service.Post`](../../internal/chat/chat.go) refuses the message when
   the agent has no model. Otherwise it finds or creates the conversation for
   the channel key (`web:<first message ID>`), stores the message as a
   `pending` submission with that UUID as its ID, and returns `202`. A
   retried request with the same UUID stores nothing.
2. `chat.Service.Run` delivers stored submissions in order. On the first
   message of a chat, it calls `createConversation` and stores the worker's
   conversation ID.
3. Control calls `submit` with the submission ID as `requestId`. pi-durable
   commits the input before it answers.
4. The worker runs the model and the tools. The browser sees the run through
   the chat's event stream (next section).
5. The worker sends `settled`. Control marks the submission `done` or
   `unanswered`, and a Telegram chat gets the answer.

## Live transcript

The contract is [`api/openapi.yaml`](../../api/openapi.yaml): schemas `Item`
and `ChatEvent`. See [ADR 0003](decisions/0003-web-ui-stack-and-live-transcript.md).

```
browser ──EventSource──▶ control  GET /api/chats/{id}/events
                           │ watch / unwatch, one watch ID per stream
                           ▼
                         worker  watchEvents() and ChatView (worker/src/view.ts)
                           │ event chat {watchId, events}
                           ▼
                         control (internal/live) adds control's messages and writes SSE
```

- [`ChatView`](../../worker/src/view.ts) keeps the raw pi-durable state,
  renders all items from it, and diffs the new items against the old ones. It
  sends `append` when only text was added, `item` for any other change, and a
  `snapshot` when an item was removed or pi-durable sent a snapshot.
- Each stream has its own worker watch, so each browser tab gets its own
  snapshot. The first event of a stream is a snapshot, so a reconnect needs no
  replay.
- [`live.Hub`](../../internal/live/live.go) adds the messages that only
  `control.sqlite` has: a `pending` message that the worker has not placed yet
  (a queued user item), and an `unanswered` message that the worker never
  placed (a user item and an error item).
- The worker's reader never waits for a stream. A stream that is 256 inputs
  behind is closed, and the browser reconnects.
- A stream sends a comment line every 15 s.

## Telegram

[`telegram.Bot`](../../internal/telegram/telegram.go) is on when the settings
have a bot token. It reads messages with `getUpdates` long polling,
so dex needs no public URL.

- Each Telegram chat is one conversation of the default agent, with the
  channel key `telegram:<chat id>`. The web UI shows it, but cannot send to
  it (403).
- The bot ignores every user who is not in the allowed users.
- The bot stores each message with `chat.Service.Submit`, which does not wait.
  The source key `telegram:<bot id>:<update id>` is unique, so a message that Telegram
  delivers again gets no second answer.
- When a submission settles, `chat.Service` calls its notify function once.
  The bot sends the answer to the chat, cut into parts of 4096 UTF-16 code
  units. A message that a restart replays gets its answer too.

## Failure behavior

- **Worker exits.** [`worker.Worker.Run`](../../internal/worker/worker.go)
  restarts it with a backoff from 1 s to 30 s. Calls wait for the new process.
  After each start, `chat.Service.Replay` sends every `pending` submission
  again with the same `requestId`. pi-durable finds the same submission and
  continues it from its last checkpoint, so no message runs twice.
- **Control exits.** Pending submissions stay in `control.sqlite`. The next
  start replays them.
- **Crash between `createConversation` and storing its ID.** The worker keeps
  an empty conversation. The next message creates a new one.
- **Worker exits while a browser watches.** Control closes every event
  stream. The browser reconnects and gets a new snapshot after the restart.
- **Model or tool error.** The submission becomes `unanswered` with a reason.
  The transcript shows an error item. A tool call that ended without a result
  shows as failed.
- **Crash between a memory edit and its commit.** The worker commits the
  change as `Recover uncommitted memory changes` when it opens the memory
  again.
- **Dream fails or stops.** Its message becomes `unanswered`, and the next
  dream, 24 h later, reads the same messages again.
- **dex not running at an automation's time.** The next check runs the newest
  missed time when it is at most 1 h old; otherwise it records that time as
  skipped. It ignores the older missed times.
- **Telegram API error.** The bot retries `getUpdates` after 5 s, from the
  first update that it did not store. A `sendMessage` that fails is logged,
  and the answer stays only in `control.sqlite`. A crash after a submission
  settles and before `sendMessage` also loses the Telegram reply.

## Security

Agent tools (`read`, `write`, `edit`, `bash`) run on the host without a
sandbox, inside the agent workspace. The HTTP server listens on
`127.0.0.1:8787` by default and has no authentication. Do not expose it to a
network. A chat channel must allow only known user IDs: the settings refuse a
Telegram bot without allowed users. The API refuses cross-site writes
(`http.CrossOriginProtection`), so another web page in the user's browser
cannot send messages or change settings.

`control.sqlite` holds provider keys and the Telegram token as plain text. The
data directory is created with mode 0700. The API never returns a key or a
token.

Every chat reads the memory, including Telegram chats of each allowed user.
A memory entry can carry a prompt injection from a web page or a file to
later chats; the rules tell the agent that memory is data, not instructions,
and git history shows each change.

Control does not log the Telegram token; Bot API errors drop the request URL,
which holds the token. The worker gets only the environment variables that
[`app.WorkerEnv`](../../internal/app/app.go) selects. It runs with its working
directory in the cache directory, and it does not load `.env` or `bunfig.toml`.

## Limits

- Harnesses stay open until shutdown.
- The memory repo is local; dex does not pull or push. Two chats that change
  the memory at the same time can land in one commit, under the source of
  the chat that settles first.
- The chat list polls every 3 s; it has no event stream.

## Simplification review

The control and worker boundary has two data owners. Control owns message
admission and channels. pi-durable owns execution and transcript history.
Combining their storage would make control depend on pi-durable internals.
Keep this boundary.

The API uses the automation service types and `settings.Agent` through
OpenAPI `x-go-type` declarations. The generated API depends on those services;
the services do not depend on HTTP types. This removes a second model and the
field conversion layer. The Go JSON tags must match the OpenAPI schema. HTTP
tests check empty arrays and optional fields at this boundary.

Provider responses keep their own API type. The service's provider input can
hold an API key. The response type cannot hold that key. Keep this conversion
as the boundary that prevents secrets from reaching the browser.

Keep the delivery queue. A pending submission can be waiting for delivery or
waiting for an answer. A loop that scans pending rows would resend messages
that the worker already accepted. Avoiding those repeats would need another
state or cursor.

Keep the live hub's merge state. It joins the worker's transcript with queued
or failed messages that only control has. Each browser stream has its own
watch and snapshot. The `seen` and `extra` maps prevent duplicate items and
unchanged updates.

The tests cover separate behavior and failure cases. Keep restart, duplicate
message, multiple browser streams, model errors, tool results, and DST cases.
Do not remove these cases to reduce the test file size. The schedule iterator tests cover time arithmetic in isolation.
