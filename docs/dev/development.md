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
2. Open <http://127.0.0.1:8787>. The setup checklist shows.
3. Add a provider: a built-in one with its API key, or a custom one with its
   API (OpenAI Chat Completions, OpenAI Responses, or Anthropic Messages), base
   URL, and key. Add more providers if you want.
4. Choose a model.

Check: a new chat answers a message, and the answer appears while the model
writes it.

To change the web UI with hot reload, run `mise run dev:server` and
`mise run dev:web` in two terminals, and open the address that `dev:web`
shows. It sends `/api/` to the server on port 8787. Run `mise run build:web`
before you commit, because the binary embeds `web/dist`.

To try the UI without a provider, start the server with `DEX_FAKE_MODEL=1`
and choose `faux/echo`, or `faux/bash` to see tool calls.

To use Telegram:

1. Create a bot with [@BotFather](https://t.me/BotFather).
2. Send a message to [@userinfobot](https://t.me/userinfobot) to get your user
   ID.
3. In Settings, enter the bot token and your user ID, and save.

Check: the bot answers a message from an allowed user.

The optional environment variables are `DEX_DATA_DIR` (default `~/.dex`) and
`DEX_ADDR` (default `127.0.0.1:8787`). Use a separate `DEX_DATA_DIR` to try
settings without changing your own.

## Tasks

Run `mise tasks` for the full list. The main tasks:

| Task              | What it does                                                        |
| ----------------- | ------------------------------------------------------------------- |
| `build`           | Builds `bin/dex` for this platform                                  |
| `build:worker`    | Compiles the worker and writes `internal/workerbin/dex-worker.gz`   |
| `check`           | All checks; the pre-commit hook and CI run it                       |
| `check:go`        | golangci-lint, `go build`, `go test -race`                          |
| `check:worker`    | `tsc`, `vp lint`, `vp fmt --check`, `bun test`                      |
| `generate:db`     | Regenerates `internal/db/sqlc`                                      |
| `generate:api`    | Regenerates the Go server and the TS client from `api/openapi.yaml` |
| `build:web`       | Builds the web UI into `web/dist`                                   |
| `check:web`       | `tsc`, `vp lint`, `vp fmt --check`, `vp test` in `web/`             |
| `check:web-dist`  | Fails when `web/dist` differs from a new build                      |
| `check:api-drift` | Fails when the generated API code differs from the contract         |

## Tests

The rules are in [testing](../../.agents/rules/testing.md). The Go tests in
`internal/app` run the real embedded worker with the faux model, and a fake
Telegram Bot API server, so run
`mise run build:worker` after a worker change. `check:go` does that for you.

## Debug

- The worker logs to the stderr of `dex`.
- Run the worker without Go: `cd worker`, then
  `DEX_DATA_DIR=$(mktemp -d) DEX_FAKE_MODEL=1 bun src/main.ts`, and type
  protocol lines, for example
  `{"id":1,"method":"createConversation","params":{"agentId":"a1","model":{"provider":"faux","modelId":"echo"}}}`.
- Read `control.sqlite` with `sqlite3 ~/.dex/control.sqlite`.
