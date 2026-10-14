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
| Agents, chats, submissions           | control             | `<data dir>/control.sqlite` ([migrations](../../internal/db/migrations/)) |
| Transcripts, runs, tool calls, usage | worker (pi-durable) | `<data dir>/agents/<id>/state.sqlite`                                     |
| Agent files                          | the agent's tools   | `<data dir>/agents/<id>/workspace/`                                       |

The default data directory is `~/.dex` (`DEX_DATA_DIR`). pi-durable allows one
process per storage, so only the worker opens an agent directory.

## Worker protocol

Control writes one JSON request per line to the worker's stdin. The worker
writes one JSON response or event per line to its stdout. Logs go to stderr.
The types are in [`worker/src/protocol.ts`](../../worker/src/protocol.ts) and
[`internal/worker/protocol.go`](../../internal/worker/protocol.go).

| Method               | Params                                              | Result           |
| -------------------- | --------------------------------------------------- | ---------------- |
| `ping`               | none                                                | `{}`             |
| `createConversation` | `agentId`, `model`, `instructions`                  | `conversationId` |
| `submit`             | `agentId`, `conversationId`, `requestId`, `content` | `submissionId`   |

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

## Security

Agent tools (`read`, `write`, `edit`, `bash`) run on the host without a
sandbox, inside the agent workspace. The HTTP server listens on
`127.0.0.1:8787` by default and has no authentication. Do not expose it to a
network. A chat channel must allow only known user IDs.

The worker gets only the environment variables that
[`app.WorkerEnv`](../../internal/app/app.go) selects. It runs with its working
directory in the cache directory, and it does not load `.env` or `bunfig.toml`.

## Limits

- Harnesses stay open until shutdown.
- A change to an agent's model applies only to new conversations.
- The web UI waits for the full answer; it does not stream.
