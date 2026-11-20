# Development

This document is for developers who build, run, and test dex.

## Set up

1. Install [mise](https://mise.jdx.dev).
2. Run `mise install`.
3. Run `mise run install`.
4. Run `mise run skills:install`.
5. Run `mise run hooks:install`.

Check: `mise run check` passes.

## Run

1. Run `mise run dev:server`. It builds the web UI and the development machine
   archives, then runs `dex --port 18787 --data-dir ~/.dex-dev`. It does not
   touch an installed dex on port 8787 or its data in `~/.dex`.
2. Open <http://127.0.0.1:18787>. The default agent timeline opens.
3. On Providers, add a built-in provider with its API key, or a custom one with its
   API (OpenAI Chat Completions, OpenAI Responses, or Anthropic Messages), base
   URL, and key. Add more providers if you want.
4. In the agent Settings tab, choose a model.

Check: a new chat answers a message, and the answer appears while the model
writes it.

To change the web UI with hot reload, run `mise run dev:server` and
`mise run dev:web` in two terminals, and open the address that `dev:web`
shows. It sends `/api/` to the server on port 18787. The binary embeds
`web/dist`. Build and Go check tasks generate it; Git does not track it.

To try the UI without a provider, start the server with `DEX_FAKE_MODEL=1`
and choose `faux/echo`, or `faux/bash` to see tool calls.

For channel setup, see the [product scope](../product.md#scope) and the
selected agent's Channels page under Settings.

Every `dev:server` run shares `~/.dex-dev`. For isolated data, such as a
second checkout or a clean first run, pass another data directory; later flags
win:

```sh
mise run dev:server -- --data-dir ~/.dex-dev-settings
mise run dev:server -- --data-dir "$(mktemp -d)" --port 18789
```

Use another `--port` when two dev servers run at once, and set `DEX_API_URL` for
`dev:web`. A machine needs an HTTPS origin, so to test the machine flow locally,
serve the dev server through `tailscale serve --bg https / http://127.0.0.1:18787`
and open the UI at that address. `mise run dev:machine-release` refreshes the
development archives after a change to `cmd/dex-agent`; see
[remote machines](../remote-machines.md). Outside `dev:server`, dex reads
`DEX_DATA_DIR` (default `~/.dex`) and `DEX_ADDR` (default `127.0.0.1:8787`).

## Legacy checkout cleanup

An old checkout can retain the ignored `worker/node_modules/` directory after
migration to the native engine. Go examples in that directory can appear in
`go list ./...`, even though dex does not depend on them.

If the removed worker directory remains, inspect it for local files first.
Remove only the obsolete `worker/node_modules/` dependency cache. Keep agent
data, `.env` files, credentials, and other local files. Do not use
`git clean -fdx` for this cleanup. The web install task adds a Go module
boundary to the current `web/node_modules/`; it does not clean old caches.

## Tasks

Run `mise tasks` for the full list. The main tasks:

| Task                | What it does                                                                                                     |
| ------------------- | ---------------------------------------------------------------------------------------------------------------- |
| `build`             | Builds `bin/dex` for this platform                                                                               |
| `check`             | All checks; CI runs its steps in jobs                                                                            |
| `check:quick`       | Static checks and the tests of changed Go packages, without `internal/app`; the pre-push hook runs it            |
| `check:go`          | golangci-lint, `go build`, `go test -race`                                                                       |
| `check:native`      | Checks removed backend paths, rejects PiG TUI and extension-host imports, and builds four platforms with cgo off |
| `generate:diagrams` | Renders the architecture and message-flow PNGs from PlantUML                                                     |
| `generate:db`       | Regenerates `internal/db/sqlc`                                                                                   |
| `generate:api`      | Regenerates the Go server and the TS client from `api/openapi.yaml`                                              |
| `build:web`         | Builds the web UI into `web/dist`                                                                                |
| `check:web`         | `tsc`, `vp lint`, `vp fmt --check`, `vp test` in `web/`                                                          |
| `check:e2e`         | Browser end-to-end tests in `web/tests/` using `e2e` and the faux provider                                       |
| `check:api-drift`   | Fails when the generated API code differs from the contract                                                      |

## Tests

The rules are in [testing](../../.agents/rules/testing.md). Go tests run the
native PiG engine with real SQLite stores. `internal/app` tests exercise HTTP,
channels, and business receipts. `internal/engine` tests cover durable input,
tools, extensions, transcript projection, and direct subscriptions. The faux
provider replaces only the model boundary. Platform tests use local HTTP or
stdio servers. They do not call production services.

Run `mise run check:go` after a Go or PiG dependency change. It builds the web
assets, runs lint, builds the packages, and runs tests with the race detector.
`mise run check:native` also builds darwin and linux for amd64 and arm64
with `CGO_ENABLED=0`. It checks that tracked backend-worker files and PiG TUI
or extension-host dependencies are absent.

Run `mise run check:e2e` to run the browser end-to-end tests in `web/tests/`.
The suite builds `web/dist`, starts `dex` with `DEX_FAKE_MODEL=1` and isolated
data in `web/.e2e/data/`, and drives the UI with `@e2e-dev/web`.

A real-provider check is separate. Use a dedicated test data directory and
credentials for that check.

## Diagrams

Edit `assets/src/architecture.puml` or `assets/src/message-flow.puml`, then run
`mise run generate:diagrams`. Inspect both PNGs in `assets/`. Keep each source
and its PNG in the same change.

## Debug

- Engine, model, and MCP errors use the dex process log. Logs must not expose
  credentials or substituted headers.
- Start `DEX_FAKE_MODEL=1 mise run dev:server -- --data-dir <dir>` to inspect
  native execution without provider credentials.
- Inspect business receipts in `control.sqlite`. Use engine history and watch
  methods to inspect execution state. Business code must not query agent
  execution tables.
- Each agent uses `<data dir>/agents/<id>/state.sqlite` and its own workspace.
  Stop dex before you copy a complete data directory for backup or inspection.

## More guides

- [CI and releases](releasing.md): CI shards, `mise run release:bump`, and the
  release workflow.
