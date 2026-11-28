# Verification

Read this before you report work as complete.

| Changed area                     | Smallest check                                                               |
| -------------------------------- | ---------------------------------------------------------------------------- |
| Go code or the PiG dependency    | `mise run check:go`                                                          |
| Migrations or queries            | `mise run check:db-drift` and `mise run check:go`                            |
| HTTP contract                    | `mise run check:api-drift`, `mise run check:go`, and `mise run check:web`    |
| Web UI                           | `mise run check:web`                                                         |
| Documents and configuration only | `mise exec -- dprint check`                                                  |
| Diagram sources                  | `mise run generate:diagrams`, inspect PNGs, then `mise exec -- dprint check` |
| Before a commit or PR            | `mise run check`                                                             |

`check:go` builds the embedded web assets, runs lint, builds Go packages,
and runs Go tests with the race detector, except `internal/db` and `internal/app`.
Run `mise run check:native` for runtime or dependency changes. It checks that
removed backend paths stay absent. It also checks the [Go dependency boundaries](../../docs/dev/architecture.md#runtime-modules)
and rejects PiG TUI, extension-host, and NSGo imports in the server. With
`CGO_ENABLED=0`, it builds the server for Darwin and Linux on amd64 and arm64.
It also builds the native app for Linux amd64.

For web UI interaction, navigation, or browser flow changes, also run
`mise run check:e2e` (runs the `e2e` browser suite in `apps/web/tests/`). `check:web`
does not run browser tests. Report a failed browser task as a verification gap
even when the aggregate check passes.

A change to a provider or to model handling also needs a manual run against a
real provider (`mise run dev:server`, with a key in Settings), because the tests use the
faux provider. Report that run, or report that you did not do it.

Report each check that you ran with its result, and each check that you did
not run with the reason.
