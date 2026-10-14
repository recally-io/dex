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
- **No mocks.** Tests run the real embedded worker, a real SQLite database, and
  a real HTTP server. `mise run check:testpolicy` rejects mock libraries.
- **Fakes only at system boundaries.** Model providers and the Telegram Bot API
  are the boundaries; `internal/app/telegram_test.go` has a fake Bot API. Tests
  use pi-ai's faux provider (`DEX_FAKE_MODEL=1`; models `faux/echo` and
  `faux/shout` answer `echo: <message>` and `shout: <message>`). A test with the faux provider does not prove that a real
  provider works; say so when you report.
- **Controlled time.** Do not use fixed sleeps. Poll the observable condition
  with a deadline, and fail with the last observed state.
- **Isolation.** Each test uses its own `t.TempDir()` for the data and cache
  directories and passes the worker an explicit environment.
- **Clean ends.** Close every `app.App` a test starts, so its worker exits.

## Tiers

| Tier   | Scope                                         | Task                        |
| ------ | --------------------------------------------- | --------------------------- |
| E2E    | Control with the real worker, through HTTP    | `check:go` (`internal/app`) |
| Worker | pi-durable storage conformance on Bun         | `check:worker` (`bun test`) |
| Manual | A real provider through `mise run dev:server` | On demand, with credentials |

Coverage: no gate exists yet. Review untested branches in the diff.

When you finish, list each test task you ran and its result. Name each check
you did not run and why.
