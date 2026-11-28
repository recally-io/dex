# Implementation

Read this before you change code, dependencies, native execution, or stored data.

## Modules

The server and native app share the root `go.mod`. The root
`internal/` tree is the shared core. It does not depend on `apps/`.

| Path                         | Owns                                                                                |
| ---------------------------- | ----------------------------------------------------------------------------------- |
| `cmd/dex`                    | Server host, service commands, and CLI                                              |
| `cmd/dex-agent`              | Outbound Machine host, enrollment, and version alignment                            |
| `internal/app`               | Wiring: business storage, engine, session service, HTTP, channels                   |
| `internal/session`           | Input intents, business receipts, child session turns and results, source state     |
| `internal/engine`            | PiG Harnesses, execution storage, model access, tool runtime, transcript projection |
| `internal/db`                | `control.sqlite`, goose migrations, sqlc queries                                    |
| `internal/server`            | HTTP API handlers of `api/openapi.yaml`, optional web handler at `/`                |
| `internal/api`               | Generated from `api/openapi.yaml` (`mise run generate:api`)                         |
| `internal/live`              | SSE transport over typed engine watches and history                                 |
| `internal/dream`             | Hidden sessions that consolidate the memory repo                                    |
| `internal/scheduler`         | Job schedules; jobs check durable state                                             |
| `internal/automation`        | Automation prompts, schedules, runs, and sessions                                   |
| `internal/runner`            | Runner leases, workspaces, backend selection, and Outbound Machine enrollment       |
| `internal/runner/outbound`   | Outbound Machine protocol and sessions                                              |
| `internal/background`        | Session Processes, output, wake-ups, and process supervision                        |
| `internal/channel`           | Channel transport contract, optional capabilities, and frozen delivery values       |
| `internal/channel/delivery`  | Bot lifecycle, channel commands, feedback previews, and durable reply outbox        |
| `internal/channel/telegram`  | Telegram polling, pairing, protocol, and media adaptation                           |
| `internal/channel/feishu`    | Feishu/Lark settings, registration, pairing, events, cards, and media adaptation    |
| `internal/channel/wechat`    | WeChat settings, QR login, polling, protocol, and media adaptation                  |
| `internal/upload`            | Upload validation, file storage, and bot-scoped paths                               |
| `internal/artifact`          | Bot artifact store and the `deliver_file` tool                                      |
| `internal/service`           | Per-user launchd/systemd definitions, commands, and readiness checks                |
| `internal/sysops`            | In-process server operations: update coordination, restart, SQLite backups, logs    |
| `internal/update`            | Release verification, maintenance coordination, and executable replacement          |
| `internal/mcptest`           | Local MCP test servers; no production execution                                     |
| `internal/settings`          | Global providers, model checks, 1Password credentials, access token                 |
| `internal/bot`               | Bot settings, activity, paths, channel approvals, and Telegram settings             |
| `internal/task`              | Tasks, the source event store, holds, waits, and the `task_*` tools                 |
| `internal/plugin`            | The seam that optional sources use: hooks, plugin API, jobs, and Task link tools    |
| `plugins/<name>`             | One optional source, such as `plugins/github`. `internal/app` does not import it    |
| `plugins/builtin`            | Plugin factories for the server and the local workspace                             |
| `apps/web`                   | React UI, built into `apps/web/dist` and embedded                                   |
| `apps/native`                | Native app entry, theme, and workspace selection                                    |
| `apps/native/internal/inapp` | Local workspace runtime with no HTTP listener                                       |

In `apps/native`, `internal/webview` owns platform WebViews and navigation.
`internal/bridge` serves local requests and streams SSE.

`internal/channel/telegram/policy` owns Telegram channel business rules.

## Rules

- Go code: follow the `use-modern-go` skill in `.agents/skills/`. New JSON
  code uses `encoding/json/v2`. IDs use the standard `uuid` package.
- Call `internal/engine.Bots` through typed Go methods. Use
  `engine.Ref{BotID, ID}` for execution references. Do not add an internal
  serialized protocol or a backend subprocess.
- Only the engine opens `<data dir>/agents/<id>/state.sqlite`. Business
  services must not query execution SQLite. Run one dex process per data
  directory. Keep PiG execution and storage calls inside `internal/engine`.
- Business packages define their own tools and prompt sections.
  `internal/app` registers the extensions and sets their session visibility.
  Use native extension descriptors and the engine's tool call context. Keep
  the existing extension names and order because PiG saves them with sessions.
- Pin the PiG module version. Provider and SDK behavior can change between
  releases. Validate model, tool, cancellation, and storage boundaries when
  you change that version.
- Native admission is the execution authority. A business receipt alone does
  not prove that input was admitted. Reconcile both failed admission and failed
  receipt writes with the same request ID. Channel delivery facts stay in
  business storage. `internal/session` owns global source deduplication through
  `ReceivedSource`. It scopes preview references to each bot through
  `LookupSource` and `SourceReceipt`. Platform adapters must not define
  duplicate input or pending replies from `admitted_at` and submission status
  predicates.
- Keep one transcript projection in the engine for history, snapshots, and
  incremental events. Store origin, attachments, and task labels in native
  input metadata. Queued input comes from the native Inbox. Withdrawn input
  uses durable display entries. Do not add a control transcript overlay.
- The engine must not import the HTTP API package. The API can use engine
  frontend types whose JSON fields match the OpenAPI contract.
- A new dependency needs the user's approval. Prefer the standard library,
  then a dependency the repository already has.
- Application settings live in `control.sqlite`. `internal/settings` owns global
  settings. `internal/bot` owns bot and Telegram settings. `internal/channel/feishu`
  owns Feishu and Lark settings. `internal/channel/wechat` owns WeChat settings.
  `internal/session` owns session overrides. Do not add an environment variable
  or a file for a setting. Skills and the encrypted secret vault use files.
  MCP declarations stay in `control.sqlite`. The settings service passes
  provider credentials directly to the engine catalog.
- Keep slow resource work outside the engine's global state lock. Track opens
  and reloads before they start. Register recovery isolation before Harness
  resume. Use a delivery gate for each Session. Never hold two Session gates or
  hold the control writer across a native call. The global receipt gate orders
  receipt commits and maintenance confirmation.
- Reload replaces only the selected bot's resources. Tools and stdio MCP
  receive an explicit environment. Do not change the dex process environment.
- Do not edit generated code: `internal/db/sqlc/` and `plugins/*/store/`
  (`mise run generate:db`), `internal/api/server.gen.go`,
  `plugins/*/api/server.gen.go`, and `apps/web/src/api/generated/`
  (`mise run generate:api`).
- `api/openapi.yaml` owns the generated HTTP contract. Change it first, then
  generate. Tag each plugin operation with the plugin name. When a schema uses
  `x-go-type`, keep its fields and the Go type's JSON tags in sync.
- A fix covers every entry point with the same failure mechanism. Search for
  the pattern, and re-check the callers, the stored data, and the tests that
  depend on the old meaning.

For Web components, follow the [Web UI rules](../../apps/web/AGENTS.md#components-and-theme).
