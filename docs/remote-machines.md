# Connect a remote machine

An Outbound Machine runs tools on a host that Dex cannot reach over SSH. The machine opens an
outbound HTTPS connection to Dex, so it needs no `sshd` and no inbound port.

Dex does not terminate TLS itself. Put it behind the same HTTPS front that serves the web UI:
nginx, Caddy, Tailscale Serve, or a cloud load balancer. The machine then trusts that front
with the system CA bundle, and the proxy configuration is the ordinary one.

A machine runs work as an account of a person by default, so a bot on that machine can read the
files and the credentials of that person. Use a Machine Runner only with Bots you trust.

## Put Dex behind HTTPS

The machine reads the installer and its token from the address of the web UI, so that address
must be an HTTPS origin without a path. Open the UI at that address and get the install command
from there.

Tailscale already has a certificate for every device, so one command is enough:

```sh
tailscale serve --bg https / http://127.0.0.1:8787
```

Dex opens the UI on `http://127.0.0.1:8787`, and Tailscale serves it at
`https://<machine>.<tailnet>.ts.net`.

nginx needs the WebSocket upgrade that every streaming service needs:

```nginx
location / {
  proxy_pass http://127.0.0.1:8787;
  proxy_http_version 1.1;
  proxy_set_header Upgrade $http_upgrade;
  proxy_set_header Connection "upgrade";
  proxy_set_header Host $host;
}
```

A machine session carries a token in a header. Do not publish Dex over plain HTTP: anyone who
can read the traffic can read that token.

## Install a machine

1. In Dex, open **Settings → Runners**. Select **Add Runner**, then **Machine → Outbound**.
2. Select **Get install command**. Dex issues a one-time code and shows the command that uses
   the address of the page. The code expires after ten minutes and works once.
3. Copy the command and run it in a terminal on the target machine, as the account that will
   use the machine. It needs no `sudo`.

The command does four things:

- It downloads `install-machine.sh` from Dex.
- It downloads the archive for the machine's platform and the running Dex version. It checks
  the SHA-256 checksum. Development builds serve their own archives.
- It installs the binary and the service of the account, then trades the one-time code for the
  machine token. Dex stores only the SHA-256 digest of that token, and the token goes to
  `device.json` in the state directory with mode `0600`.
- It starts the service. The machine comes online in Dex within a few seconds.

4. In Dex, select **Test**. Then select the Runner in the settings of one Bot, and send a
   request that runs `uname -sm`.

If the service does not start, run `dex-agent status` as that account, then
`journalctl --user -u dex-agent -n 30` on Linux or read `~/Library/Logs/dex-agent.log` on macOS.
`status` prints the mode of the machine, the account and home that it runs as, the state
directory, the workspace root, and the `PATH` that its tools use.

If the machine reconnects with a different workspace or home, Dex rejects
calls from the old lease before it sends them. In the `runner RPC` log, match
`finished` with `reason=workspace_mismatch` to `connection_wait` by `call_id`.
The event shows the elapsed time and does not include either path.

## The account that runs the work

The machine runs as the account that ran the command, so its tools are the tools of that
account: its `PATH`, the CLIs it installed, its logins for `gh`, `git`, and the cloud, and its
project directories. Nothing needs a second install or a second login for that account.

All work sent to a runner uses the permissions of that account. This includes access to its
files and credentials, such as `~/.ssh` and cloud logins. Separate Bot workspaces do not
restrict access to the home directory.

Use a separate account for shared or less trusted work.

## A dedicated account on Linux

A host that serves several people installs a dedicated account instead. Add `--dedicated` to the
command and run it as root, with `sudo`. The installer then keeps the machine in a system
account without a login of a person, and any bot may use the machine.

The two modes differ as follows:

|                 | your account (default)            | dedicated account, `--dedicated`   |
| --------------- | --------------------------------- | ---------------------------------- |
| Binary          | `~/.local/bin/dex-agent`          | `/var/lib/dex-agent/bin/dex-agent` |
| State and token | `~/.local/state/dex-agent`        | `/var/lib/dex-agent`               |
| Supervisor      | `dex-agent.service`, user manager | `dex-agent.service`, system        |
| Log             | `journalctl --user -u dex-agent`  | `journalctl -u dex-agent`          |
| Needs root      | no                                | yes                                |

The paths above are defaults. A Linux user install honors `XDG_STATE_HOME` for
state and `XDG_CONFIG_HOME` for the service file. Use those same paths when
removing the install.

Linux needs systemd with a user manager, plus curl, tar, and sha256sum or shasum. Debian 12 or
later, Ubuntu 22.04 or later, and equivalent distributions work. The account keeps the service
after it logs out, so the installer enables lingering; when it cannot, it says so and asks for
`sudo loginctl enable-linger <account>`.

The installer refuses to install as root without `--dedicated`, because root holds the tools of
no person, and a service of root can change the whole host. The user service also keeps
`NoNewPrivileges`, and the state directory stays at mode `0700`.

## macOS

The same install command works on a Mac, which installs a LaunchAgent in the login session of
the account that runs the command:

|                 | value                                     |
| --------------- | ----------------------------------------- |
| Binary          | `~/.local/bin/dex-agent`                  |
| State and token | `~/Library/Application Support/dex-agent` |
| Supervisor      | `com.recally.dex-agent` under launchd     |
| Log             | `~/Library/Logs/dex-agent.log`            |

Run the command without `sudo`. To use a separate account, log in as that account and run
the same command. `--dedicated` is only available on Linux.

```sh
launchctl print gui/$(id -u)/com.recally.dex-agent   # state of the agent
tail -f ~/Library/Logs/dex-agent.log                 # what it does
```

The state directory holds `device.json`, which contains the Dex origin, the
Runner ID (`runnerId`), and the machine token. Each Bot starts in
`workspaces/<bot-id>/workspace` in the state directory. SSH and Outbound Machines use the
same `<base>/<bot-id>/workspace` layout. The base directory depends on the
backend. All bots on a machine use the same account. Separate workspaces do not
restrict which files a bot can read or change.

To stop the agent without removing the machine:

```sh
launchctl bootout gui/$(id -u)/com.recally.dex-agent
```

## Move a machine between the two modes

The installer does not replace an installed machine without saying so. On a host that already
runs the dedicated account, the command without `--dedicated` stops, reports the existing
machine, and prints the move.

To move a dedicated install to your own account, revoke it in Dex first, then remove the system
service and its state, and run the new command as your own account. Workspaces do not move
between machines, so copy the work you need.

```sh
sudo systemctl disable --now dex-agent.service
sudo rm -rf /var/lib/dex-agent /etc/systemd/system/dex-agent.service
sudo systemctl daemon-reload
```

To move back, revoke the machine, remove the user service and its state, and run the command
with `sudo … --dedicated`:

```sh
systemctl --user disable --now dex-agent.service
rm -f ~/.config/systemd/user/dex-agent.service ~/.local/bin/dex-agent ~/.local/bin/dex-agent.previous
rm -rf ~/.local/state/dex-agent
```

## Replace, revoke, or remove a machine

Select **Revoke machine** in Dex to close the active connection and to refuse the old token.
Then stop the service and remove `device.json` from the state directory for
that install mode. Generate a new install command to enroll the Runner again.
Keep the rest of the state directory to retain the workspace.

To uninstall, first revoke the machine and move the Bot to another Runner. Keep the state
directory until you have copied the work you need: it holds the machine token and the workspace.
The machine token is in `device.json`; remove the whole state directory after the revocation.

```sh
# Linux, a machine of your own account
systemctl --user disable --now dex-agent.service
rm -f ~/.config/systemd/user/dex-agent.service ~/.local/bin/dex-agent ~/.local/bin/dex-agent.previous

# Linux, a dedicated account
sudo systemctl disable --now dex-agent.service
sudo rm -f /var/lib/dex-agent/bin/dex-agent /var/lib/dex-agent/bin/dex-agent.previous /etc/systemd/system/dex-agent.service
sudo systemctl daemon-reload

# macOS
launchctl bootout gui/$(id -u)/com.recally.dex-agent
rm -f ~/Library/LaunchAgents/com.recally.dex-agent.plist ~/.local/bin/dex-agent ~/.local/bin/dex-agent.previous
```

## Automatic version alignment

When Dex upgrades and restarts, connected `dex-agent` runners reconnect and read `X-Dex-Version`
from the server handshake. When the server reports a different release tag and the runner binary
directory is writable, `dex-agent` downloads the matching archive and `checksums.txt` from the Dex
server (`/machine/{id}/release/*`) and verifies the SHA-256 checksum and `version` output.
It retains `dex-agent.previous` before it replaces itself and re-execs in place. If re-exec
fails, it restores the previous binary.

A machine state file must contain `runnerId`. If the file does not have it,
revoke the machine in Dex and enroll it again with the current install command.

Pass `-no-auto-update` to `dex-agent run` to opt out of automatic version alignment.

For foreground use, run `dex-agent run` as the install account, or run
`/var/lib/dex-agent/bin/dex-agent run -state /var/lib/dex-agent` as the dedicated account on Linux. Without `-state`, the agent uses
`$XDG_STATE_HOME/dex-agent`, `~/.local/state/dex-agent` on Linux, or
`~/Library/Application Support/dex-agent` on macOS.

## Limits

If a connection drops after Dex sent a request, Dex cannot tell whether the command ran. Inspect
the machine before you retry. A reconnect neither undoes earlier writes nor replays an
interrupted operation.

A short network failure does not need a reload. When the machine connects again with the same
token, Dex replaces the earlier session at once. A command that starts while the machine is
offline waits up to 10 seconds for it. A revoke or a new enrollment ends the leases on the
machine, and each bot that uses it reloads.

A connection can execute up to 16 operations at a time. These operations share the target filesystem. Wait for a
write or rename to finish before starting work that depends on it. A file transfer is limited to
16 MiB. Use an SSH Machine when you need larger transfers.
