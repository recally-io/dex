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
2. Open <http://127.0.0.1:8787/#settings>.
3. Add a provider: a built-in one with its API key, or a custom one with its
   API (OpenAI Chat Completions, OpenAI Responses, or Anthropic Messages), base
   URL, and key. Add more providers if you want.
4. Choose a model and save.

Check: the Chat page answers a message.

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

| Task           | What it does                                                      |
| -------------- | ----------------------------------------------------------------- |
| `build`        | Builds `bin/dex` for this platform                                |
| `build:worker` | Compiles the worker and writes `internal/workerbin/dex-worker.gz` |
| `check`        | All checks; the pre-commit hook and CI run it                     |
| `check:go`     | golangci-lint, `go build`, `go test -race`                        |
| `check:worker` | `tsc`, `vp lint`, `vp fmt --check`, `bun test`                    |
| `generate:db`  | Regenerates `internal/db/sqlc`                                    |

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
