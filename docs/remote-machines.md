# Connect a remote machine

dex runner runs tools on a machine that Dex cannot reach over SSH. The machine opens an
outbound TLS connection to Dex, so the machine needs no `sshd` and no inbound port, and the
install command pins the Dex public key, so it needs no CA bundle and no public domain.

Use a machine environment only with bots you trust. A bot can read the files and credentials
that the service account can read.

## The machine listener

Dex opens the machine listener by itself when its host is not loopback: the port is the port
after the web UI port, so `dex serve --host 0.0.0.0 --port 8787` accepts machines on
`8788`. The web UI keeps its own socket, and Dex keeps its certificate in
`<data dir>/machine-tls.pem`. Dex creates the certificate on the first start.

A loopback-only Dex has no address that a machine can reach, so it does not open the listener.
Set the address yourself when you need another port, another interface, or a loopback UI with
a reachable machine port:

```sh
dex serve --host 127.0.0.1 --port 8787 --machine-listen 0.0.0.0:8788
```

`dex service install --machine-listen 0.0.0.0:8788` writes the flag into the service definition.

The machine port must be reachable from the target machine. A firewall rule or a NAT rule can
forward the port. A TLS proxy in front of the web UI does not serve machines: the machine
checks the Dex certificate pin, and that proxy has a different certificate.

## Install a machine

1. In Dex, open **Settings → Execution environments** and add a **dex runner**.
2. Select **Get install command**. Dex shows the address it offers to machines. Change this
   address when the machine reaches Dex through another network.
3. Copy the command and run it in a terminal on the target machine. The account needs `sudo`.
   The command expires after ten minutes and works once.

The command does four things:

- It downloads `install-machine.sh` from Dex and verifies the pinned Dex public key.
- It downloads the release archive for the machine architecture, and checks the release
  SHA-256 checksum.
- It creates the `dex-agent` system account, installs the binary and the systemd unit, and
  enrolls the machine certificate with the one-time code from the command.
- It starts the service. The machine comes online in Dex within a few seconds.

4. In Dex, select **Test**. Then select the environment in the settings of one bot, and send a
   request that runs `uname -sm`.

The target needs Linux, systemd, curl, tar, and sha256sum. Debian 12 or later, Ubuntu 22.04 or
later, and equivalent distributions work.

If the service does not start, run `sudo journalctl -u dex-agent -n 30` on the target.

## Replace, revoke, or remove a machine

Select **Revoke machine** in Dex to close the active connection and to refuse the old machine
certificate. Then generate a new install command. To connect the same machine again, stop the
service and remove `/var/lib/dex-agent/device.json` after the revocation. The workspace stays in
the state directory. Workspaces do not move between machines.

To uninstall, first revoke the machine and move the bot to another environment. On the target,
disable the systemd unit, then remove `/usr/local/bin/dex-agent` and
`/etc/systemd/system/dex-agent.service`. Keep the state directory until you have copied the work
you need. The state directory holds the machine key and the workspace.

For foreground use, run `dex-agent run -state /var/lib/dex-agent` as the `dex-agent` account.

## Limits

If a connection drops after Dex sent a request, Dex cannot tell whether the command ran. Inspect
the machine before you retry. A reconnect neither undoes earlier writes nor replays an
interrupted operation.

One machine runs one operation at a time. A file transfer is limited to 16 MiB. Use an SSH
environment when you need concurrent commands or larger transfers.
