# 0026: Execution Environment Backends and Leases

Date: 2026-11-20
Status: Accepted

Supersedes only the "Unified execution boundary" section of
[ADR 0022](0022-execution-environments.md). Its environment kinds, the control
and execution boundary, and the secret rules stay in effect.

## Context

`execenv.Manager` selected the backend with a `switch` on the kind in seven
methods. Each new kind changed all of them. The agent lifecycle API
(`CleanupSandbox`, `ResetAgent`, `CanResetAgent`) had the shape of E2B,
although each call means "the agent leaves its environment".

A handle had no stable identity. The engine read `Stamp` and then called
`Open`, and each call read `control.sqlite` again. A configuration change
between the two reads gave a handle with the wrong stamp.

Handle ownership was different for each kind. A `pi-env` handle owned its SSH
connection, while E2B and machine handles borrowed a connection. A short
engine call on an agent that was not loaded therefore made a new SSH
connection, a platform probe, and a login environment capture each time.

Machine enrollment (codes, tokens, live sessions) also lived on the Manager,
although it is a credential concern of the HTTP routes.

## Decision

- **One private backend contract.** Each remote kind (`pi-env`, `e2b`,
  `runner`) implements one package-private `backend` interface:
  `normalize`, `probe`, `open`, `changed`, `release`, `disposable`, and
  `generation`. The
  Manager keeps one map from kind to backend. `local` is not a backend: it has
  no stored configuration, so an agent without `environment_id` uses the local
  provider directly. A kind-specific operation that has no meaning for other
  kinds, such as `ForgetHostKey`, stays a direct call on its backend.
- **Backends own connections; a handle is a lease.** `Handle.Close` only
  releases the lease. `pi-env` keeps one shared connection for each
  environment. PiG `env.Connection` multiplexes requests by ID and
  serializes writes, so concurrent leases are safe. A configuration change
  or a forgotten host key retires the connection; the last lease to close it
  closes the connection. A connect that completes after `Close` fails its
  opens and closes its connection.
  E2B keeps its session for each agent, and the machine runtime keeps the
  session of each machine.
- **A handle carries its stamp.** `Handle.Stamp()` returns the fingerprint of
  the configuration that `Open` read and of the backend generation. The
  Manager computes it from the same read, before the backend opens.
  `Provider.Stamp` returns the current fingerprint, so a difference shows that
  the handle is stale. The generation covers state outside the row: a machine
  session number that dex never reuses, and a `pi-env` epoch that a forgotten
  host key advances.
- **`Handle.Home()` returns a value.** Each backend knows the home directory
  when `Open` returns. Local resolves it at `Open`. An empty home means that
  the host has no home directory.
- **One agent lifecycle API.** `ReleaseAgent` tells the backend of an earlier
  environment that the agent left it. `ResetAgent` releases the current
  environment when the backend is disposable. E2B kills the sandbox and clears
  `environment_sandbox_id` only when the row still names that sandbox, so a
  sandbox that a later open stored stays.
- **One configuration lifecycle API.** `Changed(old, cur)` replaces the
  separate update and delete hooks. A nil `cur` means that the row was deleted.
- **Machine enrollment is `execenv.Runners`.** The HTTP routes use it directly.
  The machine backend reads live sessions from it.
- **The Manager requires `Queries`.** A Manager without control storage cannot
  resolve an agent. Probes do not read storage and work without it.

## Consequences

- A new kind adds one backend and one map entry.
- Only the first `pi-env` open of an environment after a dex start or a
  configuration change pays for SSH setup. A connection stays open until its
  environment changes or dex stops. If many environments stay idle, add an idle
  close.
- A configuration change does not break an open lease. The lease keeps the
  retired connection until it closes, and the stamp difference makes the engine
  reload the agent.
- `internal/settings`, `internal/app`, `internal/server`, and `internal/engine`
  change call sites. The HTTP contract and the stored data do not change. The
  query changes make the sandbox clear conditional and read the agent and its
  environment in one statement.
