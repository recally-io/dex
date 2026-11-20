# Set up an outbound runner

A runner executes tools on a dedicated Linux host. It opens an outbound HTTPS connection to Dex. The runner does not need `sshd` or an inbound port. Use this feature only with trusted bots. The bot can access files and credentials available to the runner user.

## Prepare Dex and the target

1. Set a Dex access token. The runner setup API is unavailable without a token. Use an HTTPS reverse proxy with a certificate that the target trusts. Allow WebSocket upgrades and give long-lived connections a suitable proxy timeout. The runner does not accept a plain HTTP origin.
2. Download `dex-runner_<tag>_linux_<arch>.tar.gz` and `checksums.txt` from the same Dex release. Select `arm64` for ARM Linux or `amd64` for x86-64 Linux. Verify the archive against the matching line in `checksums.txt`, then extract it.
3. Install the binary and the included systemd unit. Create a dedicated account without sudo access.

```sh
sudo useradd --system --user-group --create-home --home-dir /var/lib/dex-runner --shell /usr/sbin/nologin dex-runner
sudo install -m 0755 dex-runner /usr/local/bin/dex-runner
sudo install -m 0644 dex-runner.service /etc/systemd/system/dex-runner.service
sudo systemctl daemon-reload
```

## Pair and assign a bot

1. In Dex, open **Execution environments** and add an **Outbound runner**.
2. Select **Generate pairing code**. Copy the environment ID and Dex HTTPS address from the displayed command. The code expires in ten minutes and works once.
3. Run the pairing command on the target under the service account. Replace `ENVIRONMENT_ID` and the example URL. Paste the code at the prompt. Do not put the code in the command line or in service configuration.

```sh
sudo -u dex-runner /usr/local/bin/dex-runner pair -state /var/lib/dex-runner -url https://dex.example -id ENVIRONMENT_ID
sudo systemctl enable --now dex-runner
sudo journalctl -u dex-runner -n 30
```

4. In Dex, select **Test** for the runner. A successful test shows its platform. Choose the environment in one bot's settings. Send a test request that runs `uname -sm`. The bot should report Linux and the target architecture.

For foreground use, run `dex-runner run` under the same account with the same `-state` directory used to pair. The runner keeps its workspace in the state directory. Do not assign the environment to a bot from another trust domain without first cleaning the target.

## Replace, revoke, or remove a runner

To replace a machine, select **Revoke device** in Dex. Dex closes the active connection and refuses the old credential. Generate a new code and pair the replacement. Workspaces do not move automatically.

To uninstall, first revoke the device and move the bot to another environment. On the target, disable the systemd unit and remove the installed binary and unit. Retain the state directory until you back up work you need. Removing the directory also removes the runner credential and workspace.

If a connection ends after a request was sent, Dex cannot tell whether the command took effect. Inspect the target before you retry it. Reconnecting the runner does not itself undo previous writes or replay interrupted operations.

The runner processes one operation at a time. File transfers have a 16 MiB limit. A long command can delay other tool calls for the same runner. Use an SSH environment if you need concurrent commands or larger transfers.
