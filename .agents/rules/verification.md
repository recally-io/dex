# Verification

Read this before you report work as complete.

| Changed area                      | Smallest check                           |
| --------------------------------- | ---------------------------------------- |
| Go code                           | `mise run check:go`                      |
| Worker TypeScript or its packages | `mise run check:worker`, then `check:go` |
| Worker protocol                   | `mise run check:worker` and `check:go`   |
| Migrations or queries             | `mise run check:db-drift` and `check:go` |
| Documents and configuration only  | `dprint check`                           |
| Before a commit or PR             | `mise run check`                         |

`check:go` rebuilds the worker binary first, because the Go tests run it.

A change to a provider or to model handling also needs a manual run against a
real provider (`mise run dev:server` with `.env`), because the tests use the
faux provider. Report that run, or report that you did not do it.

Report each check that you ran with its result, and each check that you did
not run with the reason.
