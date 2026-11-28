# Development

This document is for developers who build, run, and test dex.

## Set up

1. Install [mise](https://mise.jdx.dev).
2. Run `mise install`.
3. Run `mise run install`.
4. Run `mise run skills:install`.
5. Run `mise run hooks:install`.

Run `mise run build` to create `bin/dex`.

## Run

1. Run `mise run dev:server`. It builds the web UI and the development machine
   archives, then runs `dex --port 18787 --data-dir ~/.dex-dev`. It does not
   touch an installed dex on port 8787 or its data in `~/.dex`.
2. Open <http://127.0.0.1:18787>. The default bot timeline opens.
3. On Providers, add a built-in provider with its API key, or a custom one with its
   API (OpenAI Session Completions, OpenAI Responses, or Anthropic Messages), base
   URL, and key. Add more providers if you want.
4. In **Settings → General**, choose the default Bot's model.

Check: a new session answers a message, and the answer appears while the model
writes it.

To change the web UI with hot reload, run `mise run dev:server` and
`mise run dev:web` in two terminals, and open the address that `dev:web`
shows. It sends `/api/` to the server on port 18787. The binary embeds
`apps/web/dist`. Build and Go check tasks generate it; Git does not track it.

To try the UI without a provider, start the server with `DEX_FAKE_MODEL=1`
and choose `faux/echo`, or `faux/bash` to see tool calls.

For channel setup, see [Channels](channels.md).

Every `dev:server` run shares `~/.dex-dev`. For isolated data, such as a
second checkout or a clean first run, pass another data directory; later flags
win:

```sh
mise run dev:server -- --data-dir ~/.dex-dev-settings
mise run dev:server -- --data-dir "$(mktemp -d)" --port 18789
```

For concurrent dev servers, set different `--port` values and set
`DEX_API_URL` for each `dev:web`. To test an Outbound Machine, start the dev
server with `--require-token --stored-token` and follow the
[authenticated HTTPS setup](../remote-machines.md#put-dex-behind-https).
Run `mise run dev:machine-release` to refresh the development archives after
a change to `cmd/dex-agent` or its service templates. See the [server CLI](../../README.md#run-and-manage-dex) for defaults outside development.

## Packaging and scripts

The dex-agent service templates are in `cmd/dex-agent/packaging/`. Both
GoReleaser and the development build put the templates at the root of each
dex-agent archive. The server embeds `internal/server/install-machine.sh` and
serves it at `/install-machine.sh`. The root `install.sh` is the public raw
GitHub entry point for installing dex. Run repository tasks through mise.

## Tasks

Run `mise tasks` for the full list. The main tasks:

| Task                | What it does                                                                                     |
| ------------------- | ------------------------------------------------------------------------------------------------ |
| `build`             | Builds `bin/dex` for this platform                                                               |
| `check`             | All checks; CI runs its steps in jobs                                                            |
| `check:quick`       | Pre-push checks and changed Go package tests; see [Workflow](../../.agents/rules/workflow.md)    |
| `check:go`          | golangci-lint, `go build`, `go test -race`; `internal/db` and `internal/app` run without `-race` |
| `check:app`         | The `internal/app` tests without the race detector; CI runs them in shards with `check:go-shard` |
| `check:native`      | Checks Go dependency boundaries and builds the server and native app with cgo off                |
| `documd`            | Renders one figure: `mise run documd <source> <png>`                                             |
| `generate:diagrams` | Renders all five tracked PlantUML figures                                                        |
| `generate:db`       | Regenerates `internal/db/sqlc` and plugin stores                                                 |
| `generate:api`      | Regenerates the Go server and the TS client from `api/openapi.yaml`                              |
| `build:web`         | Builds the web UI into `apps/web/dist`                                                           |
| `check:web`         | `tsc`, `vp lint`, `vp fmt --check`, `vp test` in `apps/web/`                                     |
| `check:e2e`         | Browser end-to-end tests in `apps/web/tests/` using `e2e` and the faux provider                  |
| `check:api-drift`   | Fails when the generated API code differs from the contract                                      |

## Tests

The rules are in [testing](../../.agents/rules/testing.md). Go tests run the
native PiG engine with real SQLite stores. `internal/app` tests exercise HTTP,
channels, and business receipts. `internal/engine` tests cover durable input,
tools, extensions, transcript projection, and direct subscriptions. The faux
provider replaces only the model boundary. Platform tests use local HTTP or
stdio servers. They do not call production services.

See [Verification](../../.agents/rules/verification.md) to select checks.

Run `mise run check:e2e` to run the browser end-to-end tests in `apps/web/tests/`.
The suite builds `apps/web/dist`, starts `dex` with `DEX_FAKE_MODEL=1`, and
drives the UI with `@e2e-dev/web`. Data defaults to `apps/web/.e2e/data/`.
Set `DEX_E2E_DATA_DIR` to an empty directory for a fresh run.

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
