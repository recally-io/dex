# 0006: Per-user services and guarded binary updates

Status: accepted. Date: 2026-10-15.

## Context

dex must start at login with one command. Agent tools need the user's PATH.
The private release repository needs credentials for updates. A restart can
interrupt a shell command whose side effects cannot be replayed safely.
The new binary can also migrate local data, which makes automatic rollback
unsafe.

## Decision

Use launchd LaunchAgents on macOS and systemd user units on Linux. Do not use
root services or add a service library. Store resolved serve arguments and
PATH inside the definition. Keep a configuration record in that same file
for partial reinstall. The record is the source of boot configuration.
Read accepts older templates. Reinstall rewrites the definition from the
record and supplied flags. After an update, run `dex service install` to apply
new template defaults. The updater runs from the old executable and does not
regenerate definitions with the new template. The CLI is the only supported
writer of the definition.

Use a stable PATH symlink when it points to the current binary. Otherwise,
use the resolved executable. Reject temporary install paths. Self-update
follows symlinks and refuses obvious package-managed paths. Linux runs the
absolute dex path through `/usr/bin/env` because systemd rejects some valid
file-name characters in its executable field. env replaces itself; no shell
parses a path.

Use standard-library flags and restrict all listening addresses to loopback.
The API has no authentication and permits shell execution. Readiness must
match the version and data directory and include a successful worker ping.
A service stop gives control and its worker 60 seconds to close storage.

Use GitHub's release and release-assets APIs. Verify the exact asset checksum
and run the staged executable's version command before replacement. Strip
Authorization on cross-host redirects. Stage on the executable's filesystem
and hold a process lock through replacement and readiness. Keep the previous
inode as `dex.previous`. Do not roll back automatically.

Acquire a two-minute worker maintenance lease before replacing a running
service's binary. Worker `quiesce` closes admission before its first await.
It checks active operations, settle work through the memory commit, and
pi-durable `Harness.inspect()` for unfinished tasks and submissions. A browser
watch can resume durable work without a control submission; ping and control
reservations cannot prove that the worker is idle.

The worker owns the gate. Control stores only expiry and restores a live gate
before a restarted worker can accept requests. New submit, create, watch, and
abort operations wait before opening a harness. New messages remain in control
storage. Pending watches are cancelled by watch ID when the browser leaves.
Existing watches can continue to read. A new watch waits in full; a paused-open
harness would add another recovery path.

Busy acquire returns 409 and releases the gate. Worker failures remain errors.
POST acquires or renews; DELETE releases. The updater's executable lock and its
service-executable check serialize updates, so the lease needs no owner ID.
Expiry resumes admission after an updater crash. A control restart clears the
lease and replays pending messages. Replacement and restart have a 90-second
budget. Readiness then gets a separate 30-second budget.

Renew before restart. After restart is requested, let the lease expire on
failure: a manager command can stop the old process after it returns an error.
Releasing early could admit work during stop. RPC writer admission and pipe
writes honor cancellation. Health has a one-second worker budget; CLI probes
allow two seconds for its response.

Daily updates are opt-in. macOS runs a second LaunchAgent at 10:00 local time.
Linux uses a persistent daily timer with a random delay of up to one hour.
No definition stores credentials. Enabling updates requires gh credentials
that work with only the installed PATH and standard user variables. Manual
updates also accept GH_TOKEN and GITHUB_TOKEN. A scheduled busy update logs
and skips the attempt.

## Consequences

A reinstall keeps omitted options and restarts the service. It can select
another data directory, but it never moves data. Uninstall keeps data and
logs. A stopped service stays stopped during update. Foreground processes
need a manual restart.

macOS log files can grow without a size limit. Linux uses journald. Linux
users need lingering for operation after logout and at boot. dex prints the
command but does not enable lingering. Package installations use their
package manager for updates.

A backup requires a stopped service and a copy of the complete data
directory. Recovery must consider both binary and schema compatibility.
The previous binary alone cannot reverse a migration.
