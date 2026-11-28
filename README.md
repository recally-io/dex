# dex

dex is a small, self-hosted bot host. One Go process runs the HTTP API,
web UI, channels, and bots through [PiG](https://github.com/MichaelKinsy/PiG).
Bot state stays in local directories. The installed host needs no Node or Bun.

## Quick start

Install the latest stable release on macOS or Linux, x64 or arm64. The
installer needs `curl`, `tar`, and `sha256sum` or `shasum`:

```sh
curl -fsSL https://raw.githubusercontent.com/recally-io/dex/main/install.sh | sh
dex service install
```

The script checks the archive against `checksums.txt` and installs `dex` to
`~/.local/bin`. Set `DEX_INSTALL_DIR` for another directory, or `DEX_VERSION`
for a tag such as `v0.1.0`. Use a permanent directory: the service runs dex from it.

The service starts now and at login. Open <http://127.0.0.1:8787>.
In **Settings → Providers**, add a provider. In **Settings → General**, choose
a model for the default Bot. To build from source, see [Development](docs/dev/development.md).

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
The host must be an IP address; `localhost` becomes `127.0.0.1`. A
loopback host does not require a token by default. Do not expose that listener
through a reverse proxy without [turning on authentication](#deploy-an-https-entry-point).

### Access from other devices

Use a browser or the [native app](apps/native/README.md). A native local
workspace has its own data. Connect to the server to use the server's workspace.

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
256 base64url characters. `DEX_TOKEN` also turns on authentication for a loopback host.
The service ignores `DEX_TOKEN`. When it requires authentication, it uses the stored token.

dex serves plain HTTP, so anyone who can watch the network can read the token.
Prefer a Tailscale address to `0.0.0.0` on a shared network.

`service install` accepts `--host`, `--port`, `--data-dir`, and `--require-token`.
A reinstall keeps options that you omit and restarts dex. A new data directory selects
another directory; it does not move data. The service saves these values as
`serve` arguments. The service PATH contains only system directories and common
package-manager directories. It does not copy PATH from the install shell.
Bot tools get their environment from the interactive login shell instead.
The definition saves no credentials or other shell variables. Change the service
with the CLI.
After an update, run `dex service install` to apply new service defaults.

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

### Deploy an HTTPS entry point

Keep Dex on `127.0.0.1` and run a TLS reverse proxy on the same host. Use a
DNS name and a certificate that client devices trust. Do not expose port `8787`
through a port forward or any other listener.

1. Stop any foreground Dex instance on port `8787`. Require the stored access
   token on the loopback service:

   ```sh
   dex service install --host 127.0.0.1 --port 8787 --require-token
   ```

   For a foreground instance, run
   `dex serve --host 127.0.0.1 --port 8787 --require-token --stored-token` instead.
   If you use a custom data directory, use the same `--data-dir` with `dex token`.
2. Before you enable the proxy, run the check below. **Stop if the response is
   not `401`.** HTTPS alone does not turn on Dex authentication.

   ```sh
   curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8787/api/health
   ```
3. Add this site to your Caddyfile. Replace `dex.example.net` with your DNS
   name. Caddy gets the TLS certificate and redirects HTTP to HTTPS. Do not add
   a plain HTTP route to Dex.

   ```caddyfile
   dex.example.net {
       reverse_proxy 127.0.0.1:8787 {
           header_down Set-Cookie "^(dex_session=.*)$" "$1; Secure"
       }
   }
   ```

   The proxy adds `Secure` only to the Dex sign-in cookie. Dex already sets
   `HttpOnly` and `SameSite=Strict`. Dex does not use `X-Forwarded-Proto` to set
   the cookie. Keep the original `Host` header and do not log authorization
   headers, cookies, or sign-in bodies.
4. Run the same `curl` check against `https://dex.example.net/api/health`.
   Expect `401` without a token. Open `https://dex.example.net` in a browser and
   sign in with the token from `dex token`. Check that `dex_session` has both
   `Secure` and `HttpOnly`. HTTPS API clients use the same token as a Bearer
   token. The sign-in page and files are public; the admin API requires the token.

Keep `--require-token` enabled for as long as the proxy points to Dex. Disable
the proxy route before you remove the flag or downgrade to a version that lacks
it. Check for `401` again before you restore the route after a service or
upstream change. For a webhook-only endpoint, use the
[path-limited proxy](docs/dev/channels.md#https-and-reverse-proxy).

### Channel webhooks

Telegram uses long polling by default. Feishu and Lark use a WebSocket by
default. Neither needs a public endpoint. To opt in to webhooks, see the
[HTTPS, provider setup, and rollback guide](docs/dev/channels.md#inbound-webhooks).

## Board

Open **Board** to see Tasks across Bots. **Needs you** shows questions,
reviews, setup issues, and held events. **Updates** groups Automation results.
See [Tasks](docs/dev/tasks.md#board) for review, messages, and notes.

## Skills, MCP servers, and secrets

Open **Integrations** to find skills and add shared MCP servers. Open a Bot's
**Integrations** to see the resources that Bot uses. For file locations, reload,
and OAuth, see [Skills and Integrations](docs/dev/skills-and-integrations.md).

Add shared secrets in **Settings → Secrets**. Add Bot secrets on its **Secrets**
page. Pass [vault references](docs/dev/skills-and-integrations.md#secret-vault)
such as `vault:GITHUB_TOKEN` instead of raw values in tool arguments.

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

Archives and extracted files have a 256 MiB limit. Dex verifies the checksum
and executable version before stopping the service. Manual updates fail when
busy; daily updates log a skip. A stopped service stays stopped. Restart a
foreground process manually. See the [maintenance lease](docs/dev/architecture.md#service-and-updates).

The updater keeps the old executable as `<resolved executable>.previous`.
It does not roll back automatically, because the new version can migrate the
data. If readiness fails, read `dex service logs`. Stop the service before
recovery. Restore a compatible data backup and copy the previous executable
back to its original path, then start the service. Keep the backup until you
have checked the recovered version.

Before an upgrade, back up the complete data directory. To downgrade across a
storage migration, restore the compatible backup as well as the executable.
In particular, PiG v0.5.3 cannot read stores upgraded by PiG v0.7.0.

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
