# Implementation

Read this before you change code, dependencies, the worker protocol, or stored
data.

## Modules

| Path                 | Owns                                                             |
| -------------------- | ---------------------------------------------------------------- |
| `cmd/dex`            | Process entry: configuration from the environment, HTTP server   |
| `internal/app`       | Wiring: database, worker process, chat service, HTTP handler     |
| `internal/chat`      | Messages as durable submissions; replay after a worker restart   |
| `internal/worker`    | Worker binary install, child process, protocol client            |
| `internal/db`        | `control.sqlite`, goose migrations, sqlc queries                 |
| `internal/server`    | HTTP API under `/api/`, the web UI at `/`                        |
| `web`                | The web UI: one HTML file, embedded                              |
| `worker`             | The Bun worker: one pi-durable Harness per agent                 |
| `internal/workerbin` | The compiled worker, gzipped, embedded (`mise run build:worker`) |

## Rules

- Go code: follow the `use-modern-go` skill in `.agents/skills/`. New JSON
  code uses `encoding/json/v2`. IDs use the standard `uuid` package.
- The worker protocol is defined twice: `worker/src/protocol.ts` and
  `internal/worker/protocol.go`. Change both in one commit. The worker
  validates every request with TypeBox at its stdin boundary.
- The worker writes only protocol lines to stdout. Logs go to stderr.
- Only the worker opens an agent directory (`<data dir>/agents/<id>/`).
  pi-durable allows one process per storage.
- `@earendil-works/pi-*` packages are experimental and change their API
  between releases. Pin exact versions. Keep pi-durable calls in
  `worker/src/agents.ts` and `worker/src/models.ts`.
- A new dependency needs the user's approval. Prefer the standard library,
  then a dependency the repository already has.
- Do not edit generated code: `internal/db/sqlc/`. Run `mise run generate:db`.
- A fix covers every entry point with the same failure mechanism. Search for
  the pattern, and re-check the callers, the stored data, and the tests that
  depend on the old meaning.
