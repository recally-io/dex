# Implementation

Read this before you change code, dependencies, native execution, or stored data.

## Modules

| Path                  | Owns                                                                               |
| --------------------- | ---------------------------------------------------------------------------------- |
| `cmd/dex`             | Process entry: data directory, address, HTTP server                                |
| `internal/app`        | Wiring: business storage, engine, chat service, HTTP, channels                     |
| `internal/chat`       | Input intents, admission receipts, task records, channel delivery                  |
| `internal/engine`     | PiG Harnesses, execution storage, models, tools, extensions, transcript projection |
| `internal/db`         | `control.sqlite`, goose migrations, sqlc queries                                   |
| `internal/server`     | HTTP API handlers of `api/openapi.yaml`, the web UI at `/`                         |
| `internal/api`        | Generated from `api/openapi.yaml` (`mise run generate:api`)                        |
| `internal/live`       | SSE transport over typed engine watches and history                                |
| `internal/dream`      | Hidden chats that consolidate the memory repo                                      |
| `internal/scheduler`  | Job schedules; jobs check durable state                                            |
| `internal/automation` | Automation prompts, schedules, runs, and chats                                     |
| `internal/telegram`   | Telegram long polling, allowlist, reply delivery                                   |
| `internal/settings`   | Global providers, model checks, access token                                       |
| `internal/agent`      | Agent settings, activity, paths, channel configuration                             |
| `web`                 | React UI, built into `web/dist` and embedded                                       |

## Rules

- Go code: follow the `use-modern-go` skill in `.agents/skills/`. New JSON
  code uses `encoding/json/v2`. IDs use the standard `uuid` package.
- Call `internal/engine.Agents` through typed Go methods. Use
  `engine.Ref{AgentID, ID}` for execution references. Do not add an internal
  serialized protocol or a backend subprocess.
- Only the engine opens `<data dir>/agents/<id>/state.sqlite`. Business
  services must not query execution SQLite. Run one dex process per data
  directory. Keep PiG library calls inside `internal/engine`.
- Pin the PiG module version. Provider and SDK behavior can change between
  releases. Validate model, tool, cancellation, and storage boundaries when
  you change that version.
- Native admission is the execution authority. A business receipt alone does
  not prove that input was admitted. Reconcile both failed admission and failed
  receipt writes with the same request ID. Channel delivery facts stay in
  business storage.
- Keep one transcript projection in the engine for history, snapshots, and
  incremental events. Store origin, attachments, and task labels in native
  input metadata. Queued input comes from the native Inbox. Withdrawn input
  uses durable display entries. Do not add a control transcript overlay.
- The engine must not import the HTTP API package. The API can use engine
  frontend types whose JSON fields match the OpenAPI contract.
- A new dependency needs the user's approval. Prefer the standard library,
  then a dependency the repository already has.
- Settings live only in `control.sqlite`. `internal/settings` owns global
  settings. `internal/agent` owns agent and channel settings. `internal/chat`
  owns session overrides. Do not add an environment variable or a file for a
  setting. The exceptions are skills, MCP servers, and tool environment values
  in `.env`, which come from files
  ([ADR 0010](../../docs/dev/decisions/0010-extension-levels.md)). The settings
  service passes provider credentials directly to the engine catalog.
- Reload replaces only the selected agent's resources. Tools and stdio MCP
  receive an explicit environment. Do not change the dex process environment.
- Do not edit generated code: `internal/db/sqlc/` (`mise run generate:db`),
  `internal/api/server.gen.go` and `web/src/api/generated/`
  (`mise run generate:api`).
- `api/openapi.yaml` is the only source of the HTTP API. Change it first, then
  generate.
- A fix covers every entry point with the same failure mechanism. Search for
  the pattern, and re-check the callers, the stored data, and the tests that
  depend on the old meaning.
