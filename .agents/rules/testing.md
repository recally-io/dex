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
  in the PR.
- **No mocks.** Tests run the real native PiG engine, SQLite databases, and
  a real HTTP server. `mise run check:testpolicy` rejects mock libraries.
- **MCP servers are real.** The `internal/app` tests run
  `internal/mcptest` as a stdio server (their own test binary) or over HTTP.
- **Fakes only at system boundaries.** Model providers and the Telegram Bot API
  are the boundaries; `internal/app/telegram_test.go` has a fake Bot API. Tests
  use PiG's faux provider (`DEX_FAKE_MODEL=1`; models `faux/echo` and
  `faux/shout` answer `echo: <message>` and `shout: <message>`, and
  `faux/bash` runs the message with the bash tool and answers
  `bash: <output>`; in a dream, `faux/bash` appends the dream's JSON
  records to `dreams.md` in the memory repo; `faux/memory` answers
  `memory: <MEMORY.md section of the system prompt>`; `faux/section` answers
  `section: <the system prompt section that the message names>`; `faux/tool`
  takes `<tool name> <JSON arguments>`, loads the tool with `tool_search`,
  calls it, and answers `tool: <output>`; `faux/think` is a
  reasoning model and answers `think <thinking level>: <message>`). A test with the faux provider does not prove that a real
  provider works; say so when you report.
- **Controlled time.** Do not use fixed sleeps. Poll the observable condition
  with a deadline, and fail with the last observed state.
- **Isolation.** Each test uses its own `t.TempDir()` for the data directory
  and passes the engine an explicit tool environment. Set a separate Home
  directory when a test reads extension files. Never open production agent data.
- **Clean ends.** Close every `app.App`, `engine.Agents`, and native watch that a test
  starts. Cancellation of one subscription must not stop another subscription.

## Tiers

| Tier   | Scope                                                                                | Task                           |
| ------ | ------------------------------------------------------------------------------------ | ------------------------------ |
| E2E    | HTTP and business services with the native engine and real SQLite                    | `check:go` (`internal/app`)    |
| Engine | Native admission, recovery, tools, MCP, transcript cases, subscriptions, and history | `check:go` (`internal/engine`) |
| Manual | A real provider through `mise run dev:server`                                        | On demand, with credentials    |

Transcript regressions cover stream deltas, block replacement, tool end before
result, reset and history boundaries, task markers, queued input, and durable
withdrawal. Test direct subscriptions, reconnect, cancellation, and independent
readers through real execution storage. Do not restore tests of a removed
internal transport.

Coverage: no gate exists yet. Review untested branches in the diff.

When you finish, list each test task you ran and its result. Name each check
you did not run and why.
