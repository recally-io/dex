# Connect a remote machine

dex runner runs tools on a machine that Dex cannot reach over SSH. The machine opens an
outbound HTTPS connection to Dex, so it needs no `sshd` and no inbound port.

Dex does not terminate TLS itself. Put it behind the same HTTPS front that serves the web UI:
nginx, Caddy, Tailscale Serve, or a cloud load balancer. The machine then trusts that front
with the system CA bundle, and the proxy configuration is the ordinary one.

Use a machine environment only with bots you trust. A bot can read the files and credentials
that the service account can read.

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

1. In Dex, open **Settings → Execution environments** and add a **dex runner**.
2. Select **Get install command**. Dex issues a one-time code and shows the command that uses
   the address of the page. The code expires after ten minutes and works once.
3. Copy the command and run it in a terminal on the target machine. The account needs `sudo`.

The command does four things:

- It downloads `install-machine.sh` from Dex.
- It downloads the archive for the machine's platform and the running Dex version. It checks
  the SHA-256 checksum. Development builds serve their own archives.
- It creates the `dex-agent` system account, installs the binary and the systemd unit, and
  trades the one-time code for the machine token. Dex stores only the SHA-256 digest of that
  token, and the token goes to `/var/lib/dex-agent/device.json` with mode `0600`.
- It starts the service. The machine comes online in Dex within a few seconds.

4. In Dex, select **Test**. Then select the environment in the settings of one bot, and send a
   request that runs `uname -sm`.

The target needs curl and tar. Linux needs systemd and sha256sum or shasum; Debian 12 or later,
Ubuntu 22.04 or later, and equivalent distributions work. macOS works without sudo.

If the service does not start, run `sudo journalctl -u dex-agent -n 30` on the target.

## macOS

The same install command works on a Mac. A Mac is a single-user machine, so the agent runs in
your login session instead of as a system service:

|                 | Linux                             | macOS                                     |
| --------------- | --------------------------------- | ----------------------------------------- |
| Binary          | `/usr/local/bin/dex-agent`        | `~/.local/bin/dex-agent`                  |
| State and token | `/var/lib/dex-agent`              | `~/Library/Application Support/dex-agent` |
| Supervisor      | `dex-agent.service` under systemd | `com.recally.dex-agent` under launchd     |
| Log             | `journalctl -u dex-agent`         | `~/Library/Logs/dex-agent.log`            |

The command in the UI starts with `sudo`. On macOS you can drop it. With `sudo`, the installer
uses the account that ran `sudo`, because the agent refuses to run as root.

```sh
launchctl print gui/$(id -u)/com.recally.dex-agent   # state of the agent
tail -f ~/Library/Logs/dex-agent.log                 # what it does
```

The state directory holds `device.json`, which contains the Dex origin, the
environment ID, and the machine token. Multiple bots can use the same machine.
Each bot starts in `workspaces/<bot-id>/workspace` in the state directory. SSH
and runner use the same `<base>/<bot-id>/workspace` layout. The base directory
depends on the backend. All bots on a runner use the same account. Separate
workspaces do not restrict which files a bot can read or change.

To stop the agent without removing the machine:

```sh
launchctl bootout gui/$(id -u)/com.recally.dex-agent
```

## Replace, revoke, or remove a machine

Select **Revoke machine** in Dex to close the active connection and to refuse the old token.
Then generate a new install command. To connect the same machine again, stop the service and
remove `/var/lib/dex-agent/device.json` after the revocation. The workspace stays in the state
directory. Workspaces do not move between machines.

To uninstall, first revoke the machine and move the bot to another environment. Keep the state
directory until you have copied the work you need: it holds the machine token and the workspace.

```sh
# Linux
sudo systemctl disable --now dex-agent.service
sudo rm /usr/local/bin/dex-agent /etc/systemd/system/dex-agent.service

# macOS
launchctl bootout gui/$(id -u)/com.recally.dex-agent
rm ~/Library/LaunchAgents/com.recally.dex-agent.plist ~/.local/bin/dex-agent
```

For foreground use, run `dex-agent run -state /var/lib/dex-agent` as the `dex-agent` account on
Linux, or `dex-agent run` as your own account on macOS. Without `-state`, the agent uses
`$XDG_STATE_HOME/dex-agent`, `~/.local/state/dex-agent` on Linux, or
`~/Library/Application Support/dex-agent` on macOS.

## Limits

If a connection drops after Dex sent a request, Dex cannot tell whether the command ran. Inspect
the machine before you retry. A reconnect neither undoes earlier writes nor replays an
interrupted operation.

One machine runs one operation at a time. A file transfer is limited to 16 MiB. Use an SSH
environment when you need concurrent commands or larger transfers.
