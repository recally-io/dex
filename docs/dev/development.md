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
2. Open <http://127.0.0.1:18787>. The default bot timeline opens.
3. On Providers, add a built-in provider with its API key, or a custom one with its
   API (OpenAI Session Completions, OpenAI Responses, or Anthropic Messages), base
   URL, and key. Add more providers if you want.
4. In the bot Settings tab, choose a model.

Check: a new session answers a message, and the answer appears while the model
writes it.

To change the web UI with hot reload, run `mise run dev:server` and
`mise run dev:web` in two terminals, and open the address that `dev:web`
shows. It sends `/api/` to the server on port 18787. The binary embeds
`web/dist`. Build and Go check tasks generate it; Git does not track it.

To try the UI without a provider, start the server with `DEX_FAKE_MODEL=1`
and choose `faux/echo`, or `faux/bash` to see tool calls.

For channel setup, see the [product scope](../product.md#scope) and the
selected bot's Channels page under Settings.

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
development archives after a change to `cmd/dex-agent` or its service templates; see
[remote machines](../remote-machines.md). Outside `dev:server`, dex reads
`DEX_DATA_DIR` (default `~/.dex`) and `DEX_ADDR` (default `127.0.0.1:8787`).

## Packaging and scripts

The dex-agent service templates are in `cmd/dex-agent/packaging/`. Both
GoReleaser and the development build put the templates at the root of each
dex-agent archive. The server embeds `internal/server/install-machine.sh` and
serves it at `/install-machine.sh`. The root `install.sh` is the public raw
GitHub entry point for installing dex. Run repository tasks through mise.

## Tasks

Run `mise tasks` for the full list. The main tasks:

| Task                | What it does                                                                                                     |
| ------------------- | ---------------------------------------------------------------------------------------------------------------- |
| `build`             | Builds `bin/dex` for this platform                                                                               |
| `check`             | All checks; CI runs its steps in jobs                                                                            |
| `check:quick`       | Checks only what changed: Go tests without `-race`, skips `internal/app`; the pre-push hook runs it              |
| `check:go`          | golangci-lint, `go build`, `go test -race`; `internal/db` and `internal/app` run without `-race`                 |
| `check:app`         | The `internal/app` tests without the race detector; CI runs them in shards with `check:go-shard`                 |
| `check:native`      | Checks removed backend paths, rejects PiG TUI and extension-host imports, and builds four platforms with cgo off |
| `documd`            | Renders one figure: `mise run documd <source> <png>`                                                             |
| `generate:diagrams` | Renders the architecture, message-flow, always-on-flow, and plugins PNGs from PlantUML                           |
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
The `internal/db` and `internal/app` tests run without it.
`mise run check:native` also builds darwin and linux for amd64 and arm64
with `CGO_ENABLED=0`. It checks that tracked backend-worker files and PiG TUI
or extension-host dependencies are absent.

Run `mise run check:e2e` to run the browser end-to-end tests in `web/tests/`.
The suite builds `web/dist`, starts `dex` with `DEX_FAKE_MODEL=1` and isolated
data in `web/.e2e/data/`, and drives the UI with `@e2e-dev/web`.

A real-provider check is separate. Use a dedicated test data directory and
credentials for that check.

## Diagrams

Edit the PlantUML sources in `docs/assets/src/`, then run
`mise run generate:diagrams`. Inspect the PNGs in `docs/assets/`. Keep each
source and its PNG in the same change.

## Debug

- Engine, model, and MCP errors use the dex process log. Logs must not expose
  credentials or substituted headers.
- Start `DEX_FAKE_MODEL=1 mise run dev:server -- --data-dir <dir>` to inspect
  native execution without provider credentials.
- Inspect business receipts in `control.sqlite`. Use engine history and watch
  methods to inspect execution state. Business code must not query bot
  execution tables.
- Each bot uses `<data dir>/agents/<id>/state.sqlite` and its own workspace.
  Stop dex before you copy a complete data directory for backup or inspection.

## More guides

- [CI and releases](releasing.md): CI shards, `mise run release:bump`, and the
  release workflow.
