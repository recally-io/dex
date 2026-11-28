# Testing

Read this before you write or change a test.

## Policy

- **Test behavior.** A test states what a user or a caller observes. It does
  not assert private functions, call order, or internal state.
- **Scenario first.** Before a user-visible change, write the acceptance test.
  Name it from the user view (`TestUser...`) and write the body as
  Given / When / Then. It must fail before the change and pass after it. A bug
  fix starts with a test that reproduces the bug.
- **Prove the regression test.** Remove the fix and run the test. It must fail
  with an assertion about the target behavior. Put the command and the failure
  in the PR. Use a Go overlay or an isolated checkout for this proof. Do not
  revert source files in a shared checkout while other agents run checks.
- **No mocks.** Tests run the real native PiG engine, SQLite databases, and
  a real HTTP server. `mise run check:testpolicy` rejects mock libraries.
- **MCP servers are real.** The `internal/app` tests run
  `internal/mcptest` as a stdio server (their own test binary) or over HTTP.
- **Fakes only at system boundaries.** Use them for model providers and
  external services, such as the Telegram Bot API in `internal/app/telegram_test.go`.
  Use `DEX_FAKE_MODEL=1` for the [faux provider](../../internal/engine/fake.go).
  Use `faux/echo` for a text reply and `faux/tool` with `<tool name> <JSON arguments>`
  for a tool call. See the source for other scripts and their exact results.
  A faux-provider test does not verify a real provider.
- **Controlled time.** Do not use fixed sleeps. Poll the observable condition
  with a deadline, and fail with the last observed state.
- **Isolation.** Each test uses its own `t.TempDir()` for the data directory
  and passes the engine an explicit tool environment. Set a separate Home
  directory when a test reads extension files. Never open production bot data.
- **Clean ends.** Close every `app.App`, `engine.Bots`, and native watch that a test
  starts. Cancellation of one subscription must not stop another subscription.
- **Keep tests fast.**
  - Call `t.Parallel()` in every new Go test with isolated `t.TempDir()` state.
    Skip `t.Parallel()` only when the test mutates process-wide state
    (`t.Setenv`, `os.Chdir`, `http.DefaultClient`, or `slog.SetDefault`).
  - Open fresh control databases through `db.Open` on a new path in
    `t.TempDir()` so test binaries reuse the shared migrated SQLite template.
    Create a pre-existing database with `sql.Open` and `goose` only in
    migration upgrade tests (keep those in `internal/db`, which runs without
    `-race`).
  - Use generous positive wait deadlines (at least 10 s for real engine or MCP
    turns) so parallel `-race` tests stay deterministic under CI load.
  - Do not import `apps/web` or `internal/server` from engine or session packages;
    their CI race shards skip the frontend install and build.
  - When a CI job approaches 4–5 minutes, inspect the `.local/ci/*.jsonl`
    timing artifacts and remove the bottleneck before adding shards or raising
    timeouts.

## Tiers

| Tier   | Scope                                                                                | Task                           |
| ------ | ------------------------------------------------------------------------------------ | ------------------------------ |
| E2E    | HTTP and business services with the native engine and real SQLite                    | `check:go` (`internal/app`)    |
| Engine | Native admission, recovery, tools, MCP, transcript cases, subscriptions, and history | `check:go` (`internal/engine`) |
| Manual | A real provider through `mise run dev:server`                                        | On demand, with credentials    |

Transcript regressions cover stream deltas, block replacement, tool end before
result, reset and history boundaries, task markers, queued input, and durable
withdrawal. Test direct subscriptions, reconnect, cancellation, and independent
readers through real execution storage. Review untested branches in the diff.

When you finish, list each test task you ran and its result. Name each check
you did not run and why.
