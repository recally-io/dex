# dex

dex is a small, self-hosted bot host. One Go process runs the HTTP API,
web UI, channels, and bots through [PiG](https://github.com/MichaelKinsy/PiG).
Bot state stays in local directories. The installed host needs no Node or Bun.

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
On the Providers page, add a provider. On the Bot page, choose a model.

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
`serve` arguments. The service PATH contains only system directories and common
package-manager directories. It does not copy PATH from the install shell.
Bot tools get their environment from the interactive login shell instead.
The definition saves no credentials or other shell variables. Change the service
with the CLI.
The configuration record is the source of boot options. Reads accept definitions
from earlier releases. After an update,
run `dex service install` to apply new template defaults.

Stop any foreground dex process on the same address before you install.
Install and start wait up to 30 seconds for the expected version, data
directory, and engine readiness. `dex version` shows the version, commit, platform,
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

For a dedicated Linux machine that cannot accept SSH, see
[Connect a remote machine](docs/remote-machines.md).

### Channel webhooks

Telegram uses long polling by default. Feishu and Lark use a WebSocket by
default. Neither needs a public endpoint. To opt in to webhooks, see the
[HTTPS, provider setup, and rollback guide](docs/dev/channels.md#inbound-webhooks).

## Board

Open **Board** to see Tasks across Bots. **Needs you** shows questions,
reviews, setup issues, and held events. **Updates** groups Automation results.
Open a Task to send a message to its Assignee's Main or add a note.
See [Tasks](docs/dev/tasks.md).

## Skills and MCP servers

dex reads skills from these directories:

| Scope   | Skills                                              |
| ------- | --------------------------------------------------- |
| Dex     | `~/.dex/.agents/skills/<name>/SKILL.md`             |
| Bot     | `~/.dex/agents/<id>/.agents/skills/<name>/SKILL.md` |
| Project | `<cwd>/.agents/skills/<name>/SKILL.md`              |

dex does not read `~/.agents`. A narrower entry replaces the wider entry with the same name; Project is the
narrowest. Main and Sessions, including child Sessions, can select a project
directory. The picker browses folders on the Session's Runner and lists recent projects for the
current bot. Use Up or Home to navigate, or enter a path and press Enter.
Select **Use this folder** to save the current folder. The `set_project` tool
can also set the directory. The project cwd defaults to the bot workspace,
`~/.dex/agents/<id>/workspace`. Clear the project to restore that default.
A child Session can inherit its parent's project when it uses the same Runner.
See [Sessions](docs/dev/sessions.md#session-runners).
Dex paths follow `DEX_DATA_DIR` when you change the data directory.

A skill is a directory with a `SKILL.md` that starts with YAML front matter
with a `description` and an optional `name`. Without a `name`, the directory
name is the skill name. A skill can be in a nested directory, such as
`skills/team/review/SKILL.md`. dex does not look inside a skill, hidden
directories, or `node_modules`. When two skills in one level have the same
name, the first in path order wins, and the bot's Skills page shows a warning.
The prompt lists each skill. The bot reads the file when a task needs it.
An edit to a skill applies to the next message.

Ask the bot in an owner session to manage MCP declarations with the
`mcp_server_*` tools. Use vault references for secrets. dex stores declarations
in `control.sqlite` and shows their status in the bot's Integrations page.
Changes apply after the bot finishes its current work. dex ignores old
`mcp.json` files. See [MCP declarations](docs/dev/skills-and-integrations.md#mcp-declarations).

For an OAuth-enabled remote MCP server, ask the bot to add an HTTP declaration
with `oauth: {}` and no static headers. If the provider requires a registered
client, use its client ID and a vault reference for its secret. In the bot's
Integrations page, select **Authorize** and sign in with the provider. See
[MCP browser authorization](docs/dev/skills-and-integrations.md#mcp-browser-authorization)
for the callback URL and client settings.

## Secrets

Add shared secrets in **Settings → Secrets**. Add secrets for one bot on
its **Secrets** page. dex encrypts the values with age and never shows them in
Settings. Tell the bot to use a reference such as `vault:GITHUB_TOKEN` in the
`env` argument of `bash`, a Monitor, or an MCP declaration. You do not need the
1Password CLI.

dex ignores old `.env` files. It does not import them. Move secrets to the
[vault](docs/dev/skills-and-integrations.md#secret-vault) and put other environment values
in shell rc files or in the `env` of a call or MCP server.

## Updates

```sh
dex update --check
dex update
dex update --channel prerelease
dex update --version v0.1.0-rc.1
dex service install --auto-update --channel prerelease
```

Updates come from the `recally-io/dex` public GitHub repository. Manual and
Web UI updates (Settings → About) use `GH_TOKEN`, then `GITHUB_TOKEN`, then
stored `gh auth` credentials when available, and fall back to unauthenticated
public release downloads otherwise. Automatic scheduled updates require `gh`
and credentials in its default store. When you enable automatic updates,
install adds the selected `gh` directory to the service PATH.
It does not add other directories from the install shell. If you move or remove
that `gh` installation, run `dex service install` again.
Run `gh auth login --hostname github.com` first. Shell-only tokens cannot enable
automatic updates. dex never saves a token in a definition or log.

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
no work is active. During the lease, web input returns `409` before dex stores
a receipt. Retry the same request ID after release or expiry. A web request
whose receipt committed before lease confirmation can return `202` and remain
pending. Channel input can also keep a pending receipt. Pending input resumes
admission after maintenance. Manual
updates fail when busy; daily updates log a skip. A stopped service stays
stopped. Foreground processes need a manual restart.

The updater keeps the old executable as `<resolved executable>.previous`.
It does not roll back automatically, because the new version can migrate the
data. If readiness fails, read `dex service logs`. Stop the service before
recovery. Restore a compatible data backup and copy the previous executable
back to its original path, then start the service. Keep the backup until you
have checked the recovered version.

PiG v0.7.0 upgrades the native execution format in each bot's `state.sqlite`.
Before the first start with this version, stop dex and back up the complete
data directory. A build that uses PiG v0.5.3 cannot read the upgraded format.
To return to that build, restore the backup as well as the executable.

## Backup and uninstall

Run `dex service stop` before you copy the complete data directory to a backup.
Then run `dex service start`. A stopped dex process has closed its SQLite files.

Run `dex service uninstall` to stop dex and remove its service and update job.
Uninstall keeps the data directory and logs. A second uninstall succeeds.
There is no purge command.

## Documents

- [Changelog](CHANGELOG.md): release history and upcoming changes.
- [Product](docs/product.md): users, scope, and non-goals.
- [Development](docs/dev/development.md): building, testing, and debugging from source.
