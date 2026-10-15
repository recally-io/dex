# dex

dex is a small, self-hosted agent host. One Go binary runs a control process
(HTTP API, web UI, and Telegram) and an embedded Bun worker that
runs the agents on [pi-durable](https://www.npmjs.com/package/@earendil-works/pi-durable).
The agent state stays in local directories. The host needs no Node or Bun.

## Quick start

Install the latest release on macOS or Linux, x64 or arm64:

```sh
curl -fsSL https://raw.githubusercontent.com/recally-io/dex/main/install.sh | sh
dex service install
```

The script checks the archive against `checksums.txt` and installs `dex` to
`~/.local/bin`. Set `DEX_INSTALL_DIR` for another directory, or `DEX_VERSION`
for a tag such as `v0.1.0`. Use a permanent directory: the service runs dex from it.

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
The host must be an IP address; `localhost` becomes `127.0.0.1`. On a
loopback host the API has no authentication.

### Access from other devices

The API can run shell commands, so a host that is not loopback requires an
access token:

```sh
dex service install --host 0.0.0.0   # or a Tailscale address such as 100.64.0.7
dex token                            # print the token
```

Open `http://<host address>:8787` on the other device and paste the token.
The browser keeps it in a cookie. API clients send `Authorization: Bearer <token>`.
dex creates the stored token on its first start and never replaces it.
For a foreground `dex serve`, set `DEX_TOKEN` to your own random token of 32 to
256 base64url characters. DEX_TOKEN also turns on the token for a loopback host.
The service ignores DEX_TOKEN and always uses the stored token.

dex serves plain HTTP, so anyone who can watch the network can read the token.
Prefer a Tailscale address to `0.0.0.0` on a shared network.

`service install` accepts `--host`, `--port`, and `--data-dir`. A reinstall
keeps options that you omit and restarts dex. A new data directory selects
another directory; it does not move data. The service saves these values as
`serve` arguments. It also saves PATH from the install shell. It saves no
credentials or other shell variables. Change the service with the CLI.
The configuration record is the source of boot options. Reads accept definitions
from earlier releases. After an update,
run `dex service install` to apply new template defaults.

Stop any foreground dex process on the same address before you install.
Install and start wait up to 30 seconds for the expected version, data
directory, and worker ping. `dex version` shows the version, commit, platform,
and executable. Each command supports `--help`.

- macOS uses `~/Library/LaunchAgents/io.recally.dex.plist`.
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

## Skills and MCP servers

dex reads skills and MCP servers from files, for all agents and for one agent:

| Scope  | Skills                               | MCP servers                   |
| ------ | ------------------------------------ | ----------------------------- |
| Global | `~/.agents/skills/<name>/SKILL.md`   | `~/.agents/mcp.json`          |
| Agent  | `~/.dex/agents/<id>/skills/<name>/…` | `~/.dex/agents/<id>/mcp.json` |

An agent entry replaces a global entry with the same name. A skill is a
directory with a `SKILL.md` that starts with YAML front matter with a
`description`. The prompt lists each skill; the agent reads the file when a
task needs it. `mcp.json` has an `mcpServers` object, as in Claude Code and
Cursor:

```json
{
  "mcpServers": {
    "files": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "."]
    },
    "github": {
      "type": "http",
      "url": "https://api.githubcopilot.com/mcp/",
      "headers": { "Authorization": "Bearer <token>" }
    },
    "old": { "disabled": true }
  }
}
```

A stdio server gets `command`, `args`, `env`, and `cwd` (relative to the agent
workspace). An HTTP server gets `url` and `headers`. OAuth is not supported.

An edit to a skill applies to the next message. After you edit mcp.json,
select Reload MCP servers on the Extensions page. An MCP server runs as your
user without a sandbox, and mcp.json keeps its credentials as plain text.

## Updates

```sh
dex update --check
dex update
dex update --channel prerelease
dex update --version v0.1.0-rc.1
dex service install --auto-update --channel prerelease
```

Updates come from the `recally-io/dex` GitHub repository. Manual updates
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
