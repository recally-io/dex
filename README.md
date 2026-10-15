# dex

dex is a small, self-hosted agent host. One Go binary runs a control process
(HTTP API, web UI, and Telegram) and an embedded Bun worker that
runs the agents on [pi-durable](https://www.npmjs.com/package/@earendil-works/pi-durable).
The agent state stays in local directories. The host needs no Node or Bun.

## Quick start

Put the release binary `dex` in a permanent location on your PATH. Then run:

```sh
dex service install
```

The command starts dex now and at login. Open <http://127.0.0.1:8787>.
On the Providers page, add a provider. On the Agent page, choose a model.

To build from source, run `mise install`, `mise run install`, and
`mise run build`. The binary is `bin/dex`. See [Development](docs/dev/development.md).

## Run and manage dex

```sh
dex serve --host 127.0.0.1 --port 8787 --data-dir ~/.dex
dex service status --json
dex service logs -n 100 -f
dex service stop
dex service start
dex service restart
dex open
dex doctor
```

`dex` with no arguments runs `serve` in the foreground. Flags override
`DEX_ADDR` and `DEX_DATA_DIR`, which override the defaults shown above.
Only loopback hosts are allowed: `127.0.0.0/8` and `::1`. `localhost` becomes
`127.0.0.1`. The API has no authentication and can run shell commands.

`service install` accepts `--host`, `--port`, and `--data-dir`. A reinstall
keeps options that you omit and restarts dex. A new data directory selects
another directory; it does not move data. The service saves these values as
`serve` arguments. It also saves PATH from the install shell. It saves no
credentials or other shell variables. Change the service with the CLI; dex refuses a definition that
has been edited outside dex.

Stop any foreground dex process on the same address before you install.
Install and start wait up to 30 seconds for the expected version, data
directory, and worker ping. `dex version` shows the version, commit, platform,
and executable. Each command supports `--help`.

- macOS uses `~/Library/LaunchAgents/com.vaayne.dex.plist`.
  Logs go to `~/Library/Logs/dex/dex.log`. The log has no size limit.
  launchd restarts dex after a crash or a nonzero exit. A clean exit does not
  restart it. Stop removes the job from launchd but keeps the definition.
- Linux uses `$XDG_CONFIG_HOME/systemd/user/dex.service`, or
  `~/.config/systemd/user/dex.service` when XDG_CONFIG_HOME is unset.
  Logs are in `journalctl --user -u dex`. A systemd user manager is required.
  To keep dex running after logout and at boot, run `loginctl enable-linger "$USER"`.
  Install prints a hint when lingering is off; it does not enable it.

The service uses an absolute executable path. If `dex` on PATH points to the
current binary, dex retains that path, including a Homebrew symlink. Otherwise,
it uses the resolved path. If that path changes, run install from the new
binary. Temporary builds, including `go run`, cannot install a service.
Other operating systems are not supported.

## Updates

```sh
dex update --check
dex update
dex update --channel prerelease
dex update --version v0.1.0-rc.1
dex service install --auto-update --channel prerelease
```

Updates come from the private `vaayne/dex` GitHub repository. Manual updates
use GH_TOKEN, then GITHUB_TOKEN, then stored `gh auth` credentials. Automatic
updates require `gh` on the installed PATH and credentials in its default
store. Run `gh auth login --hostname github.com` first. Shell-only tokens
cannot enable automatic updates. dex never saves a token in a definition or log.

Automatic updates are off by default. macOS checks daily at 10:00 local time.
Linux uses a persistent daily timer with a random delay of up to one hour.
Use `service install --auto-update=false` to turn updates off. The stable
channel excludes prereleases. If only a release candidate exists, stable
reports that no stable release is available. Same-version and older releases
leave the binary in place and report `up to date`. Dev builds and obvious
package-managed paths cannot use self-update; use the build or package manager.

Archives and extracted files have a 256 MiB size limit.
An update verifies the archive checksum and executable version before it stops
anything. A running service enters a two-minute maintenance lease only when
no work is active. New work stays queued until release or restart. Manual
updates fail when busy; daily updates log a skip. A stopped service stays
stopped. Foreground processes need a manual restart.

The updater keeps the old executable as `<resolved executable>.previous`.
It does not roll back automatically, because the new version can migrate the
data. If readiness fails, read `dex service logs`. Stop the service before
recovery. Restore a compatible data backup and copy the previous executable
back to its original path, then start the service. Keep the backup until you
have checked the recovered version.

## Backup and uninstall

Run `dex service stop` before you copy the complete data directory to a backup.
Then run `dex service start`. A stopped worker has closed its SQLite files.

Run `dex service uninstall` to stop dex and remove its service and update job.
Uninstall keeps the data directory and logs. A second uninstall succeeds.
There is no purge command.

## Documents

- [Product](docs/product.md): users, scope, and non-goals.
- [Architecture](docs/dev/architecture.md): how control and the worker fit
  together.
- [Development](docs/dev/development.md): tasks, tests, and debugging.
- [Decisions](docs/dev/decisions/): ADRs.
- [Agent instructions](AGENTS.md).
