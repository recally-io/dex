# Architecture

This document is for developers who change dex. It describes the subsystems,
their sources of truth, the message flow, and the failure behavior.

## Processes

dex is one binary with two processes:

| Process | Code                         | Responsibilities                                                                 | Does not                    |
| ------- | ---------------------------- | -------------------------------------------------------------------------------- | --------------------------- |
| control | Go: `cmd/dex`, `internal/`   | HTTP API and web UI, chat channels, timers, `control.sqlite`, the worker process | run agents or call models   |
| worker  | TypeScript on Bun: `worker/` | one pi-durable Harness per agent, model calls, tool calls                        | know about channels or time |

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

The default data directory is `~/.dex` (`DEX_DATA_DIR`). pi-durable allows one
process per storage, so only the worker opens an agent directory.

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

| Method               | Params                                                                       | Result                    |
| -------------------- | ---------------------------------------------------------------------------- | ------------------------- |
| `ping`               | none                                                                         | `{}`                      |
| `createConversation` | `agentId`, `model`, `instructions`                                           | `conversationId`          |
| `submit`             | `agentId`, `conversationId`, `model`, `instructions`, `requestId`, `content` | `submissionId`            |
| `configure`          | `providers`: `id`, `name`, `api`, `baseUrl`, `apiKey`, `models`              | `errors` per provider     |
| `listProviders`      | none                                                                         | built-in `providers`      |
| `listModels`         | none                                                                         | `models` with credentials |

The worker runs the submits of one conversation in order. When `model` or
`instructions` differ from the conversation's, it changes the conversation
first; a run in progress keeps its settings until its next turn.

Event `settled`: `requestId`, `status` (`done` or `unanswered`), `answer` or
`reason`. The worker sends it when a submission has its final answer.

## Message flow

![Message flow](../../assets/message-flow.png)

Source: [message-flow.puml](../../assets/src/message-flow.puml)

1. [`chat.Service.Send`](../../internal/chat/chat.go) finds or creates the
   conversation for the channel key (for example `web:default`), and stores
   the message as a `pending` submission.
2. On the first message of a chat, control calls `createConversation` and
   stores the worker's conversation ID.
3. Control calls `submit` with the submission ID as `requestId`. pi-durable
   commits the input before it answers.
4. The worker runs the model and the tools, then sends `settled`. Control
   marks the submission `done` or `unanswered` and returns the answer.

## Telegram

[`telegram.Bot`](../../internal/telegram/telegram.go) is on when the settings
have a bot token. It reads messages with `getUpdates` long polling,
so dex needs no public URL.

- Each Telegram chat is one conversation of the default agent, with the
  channel key `telegram:<chat id>`.
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
- **Model or tool error.** The submission becomes `unanswered` with a reason.
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

Control does not log the Telegram token; Bot API errors drop the request URL,
which holds the token. The worker gets only the environment variables that
[`app.WorkerEnv`](../../internal/app/app.go) selects. It runs with its working
directory in the cache directory, and it does not load `.env` or `bunfig.toml`.

## Limits

- Harnesses stay open until shutdown.
- A change to an agent's model applies only to new conversations.
- The web UI waits for the full answer; it does not stream.
