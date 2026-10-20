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

1. Run `mise run dev:server`.
2. Open <http://127.0.0.1:8787>. The default agent timeline opens.
3. On Providers, add a built-in provider with its API key, or a custom one with its
   API (OpenAI Chat Completions, OpenAI Responses, or Anthropic Messages), base
   URL, and key. Add more providers if you want.
4. In the agent Settings tab, choose a model.

Check: a new chat answers a message, and the answer appears while the model
writes it.

To change the web UI with hot reload, run `mise run dev:server` and
`mise run dev:web` in two terminals, and open the address that `dev:web`
shows. It sends `/api/` to the server on port 8787. The binary embeds
`web/dist`. Build and Go check tasks generate it; Git does not track it.

To try the UI without a provider, start the server with `DEX_FAKE_MODEL=1`
and choose `faux/echo`, or `faux/bash` to see tool calls.

For channel setup, see the [product scope](../product.md#scope) and the
selected agent's Channels tab.

The optional environment variables are `DEX_DATA_DIR` (default `~/.dex`) and
`DEX_ADDR` (default `127.0.0.1:8787`). Use a separate `DEX_DATA_DIR` to try
settings without changing your own.

## macOS desktop preview

The Wails 3 desktop preview lives in `desktop/wails3/`. It uses the existing
React/Vite build and loads Dex through a loopback HTTP URL. The desktop window
reuses an existing service with the same data directory or starts a local
backend. Its loopback proxy supplies the stored service token internally, so
the WebView does not show the token sign-in flow.

Run the preview on macOS:

```sh
cd desktop/wails3
mise exec -- env GOBIN=/tmp/dex-wails3-bin go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.33
PATH=/tmp/dex-wails3-bin:$PATH mise exec -- go mod tidy
PATH=/tmp/dex-wails3-bin:$PATH mise exec -- wails3 package
open bin/Dex.app
```

The package is ad hoc signed for local testing. Production signing,
notarization, and update distribution are separate release work.

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
| `check`             | All checks; the pre-commit hook runs it, CI runs its steps in jobs                                               |
| `check:go`          | golangci-lint, `go build`, `go test -race`                                                                       |
| `check:native`      | Checks removed backend paths, rejects PiG TUI and extension-host imports, and builds four platforms with cgo off |
| `generate:diagrams` | Renders the architecture and message-flow PNGs from PlantUML                                                     |
| `generate:db`       | Regenerates `internal/db/sqlc`                                                                                   |
| `generate:api`      | Regenerates the Go server and the TS client from `api/openapi.yaml`                                              |
| `build:web`         | Builds the web UI into `web/dist`                                                                                |
| `check:web`         | `tsc`, `vp lint`, `vp fmt --check`, `vp test` in `web/`                                                          |
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

A real-provider check is separate. Use a dedicated test data directory and
credentials for that check.

## Diagrams

Edit `assets/src/architecture.puml` or `assets/src/message-flow.puml`, then run
`mise run generate:diagrams`. Inspect both PNGs in `assets/`. Keep each source
and its PNG in the same change.

## CI and releases

[Check](../../.github/workflows/check.yml) runs on each push to `main` and
each pull request. Its jobs run the steps of `mise run check` in parallel:
lint and drift, web, and Go lint and tests. When you add a step to
`check` in `mise.toml`, add it to the workflow too.

To release:

1. Make sure that Check passes on `main`.
2. Run `git tag v0.1.0` and `git push origin v0.1.0`. A tag with a hyphen,
   such as `v0.1.0-rc.1`, makes a prerelease.

[Release](../../.github/workflows/release.yml) runs Check, then
[GoReleaser](../../.goreleaser.yaml) on macOS. GoReleaser builds `dex` for
Linux and macOS, x64 and arm64, and uploads a draft release with one
`dex_<tag>_<os>_<arch>.tar.gz` for each platform, `checksums.txt`, and notes
from the pull requests. GoReleaser builds the web UI once and embeds it in each Go binary. Each
binary starts on its own platform as a smoke test.
When all smoke tests pass, the workflow publishes the draft. dex reads its
version from the tag.

To try the build on your machine, run
`goreleaser release --snapshot --clean --parallelism 1`. The output goes to
`dist/`.

The macOS binaries are signed ad hoc, not notarized. macOS blocks a binary
from a browser download; run `xattr -d com.apple.quarantine dex` first.

## Debug

- Engine, model, and MCP errors use the dex process log. Logs must not expose
  credentials or substituted headers.
- Start `mise run dev:server` with a separate `DEX_DATA_DIR` and
  `DEX_FAKE_MODEL=1` to inspect native execution without provider credentials.
- Inspect business receipts in `control.sqlite`. Use engine history and watch
  methods to inspect execution state. Business code must not query agent
  execution tables.
- Each agent uses `<data dir>/agents/<id>/state.sqlite` and its own workspace.
  Stop dex before you copy a complete data directory for backup or inspection.
