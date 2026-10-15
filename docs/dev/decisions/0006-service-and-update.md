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
for partial reinstall. Check that the generated definition matches the file
before using it. The CLI is the only supported writer of the definition.

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

Acquire a two-minute maintenance lease before replacing a running service's
binary. The dispatch lock checks active submissions and grants the lease as
one operation. New messages, automations, and dreams stay stored while the
lease holds dispatch. Busy acquire returns 409 and holds no gate. A UUID
identifies the owner. Renewal uses the same UUID; release checks ownership.
Expiry resumes dispatch after an updater crash. Replacement and restart
have a 90-second deadline, within the lease. A process restart clears the
lease and replays pending messages.
Renew the lease before requesting restart. After that request, let the lease
expire on failure: a service-manager command can stop the old process after
the command returns an error. Releasing early could dispatch work during stop.

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
