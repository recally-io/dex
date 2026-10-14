# dex

dex is a small, self-hosted agent host. One Go binary runs a control process
(HTTP API, web UI, and later chat channels) and an embedded Bun worker that
runs the agents on [pi-durable](https://www.npmjs.com/package/@earendil-works/pi-durable).
The agent state stays in local directories. The host needs no Node or Bun.

## Quick start

1. Run `mise install && mise run install`.
2. Run `cp .env.example .env`, and set `DEX_MODEL` and the provider variables.
3. Run `mise run dev:server`, then open <http://127.0.0.1:8787>.

`mise run build` writes a single binary to `bin/dex`.

## Documents

- [Product](docs/product.md): users, scope, and non-goals.
- [Architecture](docs/dev/architecture.md): how control and the worker fit
  together.
- [Development](docs/dev/development.md): tasks, tests, and debugging.
- [Decisions](docs/dev/decisions/): ADRs.
- [Agent instructions](AGENTS.md).
