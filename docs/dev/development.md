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

1. Run `cp .env.example .env`.
2. Set `DEX_MODEL` to `<provider>/<model ID>`. For an OpenAI-compatible
   gateway, set `OPENAI_BASE_URL` and `OPENAI_API_KEY`, and use the provider
   `openai-compatible`.
3. Run `mise run dev:server`.

Check: <http://127.0.0.1:8787> answers a message.

Other providers read their usual variables, for example `ANTHROPIC_API_KEY`.

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
`internal/app` run the real embedded worker with the faux model, so run
`mise run build:worker` after a worker change. `check:go` does that for you.

## Debug

- The worker logs to the stderr of `dex`.
- Run the worker without Go: `cd worker`, then
  `DEX_DATA_DIR=$(mktemp -d) DEX_FAKE_MODEL=1 bun src/main.ts`, and type
  protocol lines, for example
  `{"id":1,"method":"createConversation","params":{"agentId":"a1","model":{"provider":"faux","modelId":"echo"}}}`.
- Read `control.sqlite` with `sqlite3 ~/.dex/control.sqlite`.
