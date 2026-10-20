# Verification

Read this before you report work as complete.

| Changed area                     | Smallest check                                                            |
| -------------------------------- | ------------------------------------------------------------------------- |
| Go code or the PiG dependency    | `mise run check:go`                                                       |
| Migrations or queries            | `mise run check:db-drift` and `mise run check:go`                         |
| HTTP contract                    | `mise run check:api-drift`, `mise run check:go`, and `mise run check:web` |
| Web UI                           | `mise run check:web`                                                      |
| Documents and configuration only | `dprint check`                                                            |
| Diagram sources                  | `mise run generate:diagrams`, inspect PNGs, then `dprint check`           |
| Before a commit or PR            | `mise run check`                                                          |

`check:go` builds the embedded web assets, runs lint, builds Go packages,
and runs Go tests with the race detector. There is no backend worker build.
Run `mise run check:native` for runtime or dependency changes. It checks that
removed backend paths stay absent, rejects PiG TUI and extension-host imports,
and builds darwin/linux on amd64/arm64 with `CGO_ENABLED=0`.

A change to a provider or to model handling also needs a manual run against a
real provider (`mise run dev:server`, with a key in Settings), because the tests use the
faux provider. Report that run, or report that you did not do it.

Report each check that you ran with its result, and each check that you did
not run with the reason.
