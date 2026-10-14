# 0003: A React web UI on an OpenAPI contract, with a live transcript over SSE

Status: Accepted (2026-10-14)

## Context

The first web UI was one HTML file: one chat, no history after a reload, no
streaming, and no view of tool calls. Tool calls run on the host without a
sandbox, so the user must see them while they run. The agent-kit project
setup defines a web stack for a Go service with a UI.

## Decision

- The web UI is a React project in `web/` (Vite+, shadcn on Base UI,
  Tailwind CSS, Beautiful UI tokens, TanStack Router and Query). Its build,
  `web/dist`, is committed and embedded, so `go build` needs no Node.
- [`api/openapi.yaml`](../../../api/openapi.yaml) is the only source of the
  HTTP API. oapi-codegen generates the Go strict server
  (`internal/api/server.gen.go`); @hey-api/openapi-ts generates the TypeScript
  client. `mise run check:api-drift` fails when the generated code is old.
- The transcript comes to the browser as server-sent events. The worker turns
  pi-durable agent events into display items (`Item`) and changes
  (`ChatEvent`); the browser never sees pi-durable types. Each stream starts
  with a snapshot, so a reconnect needs no replay protocol.
- A message has one ID from the browser to pi-durable: the browser makes a
  UUID, control uses it as the submission ID, and the worker places the input
  as a `dex.user` entry with it. The browser shows its message at once and
  replaces it when the transcript has it.

## Consequences

- The UI shows tokens as they arrive, tool calls with their output, and
  failed and stopped runs.
- A change to the API is a change to `api/openapi.yaml` first, then
  `mise run generate:api`.
- A pi-durable release can change its agent events
  (`watchEvents` is experimental). `worker/test/view.test.ts` holds golden
  cases for the event shapes that dex reads.
- The web build adds Node tooling to development, not to the dex binary.
