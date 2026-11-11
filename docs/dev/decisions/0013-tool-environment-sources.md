# 0013: Login shell environment, 1Password CLI, and automatic reload

Status: Accepted (2026-11-11). Amends [ADR 0010](0010-extension-levels.md).

## Context

ADR 0010 gives tools a base environment of six process keys (`PATH`, `HOME`,
`TMPDIR`, `LANG`, `USER`, `SHELL`) plus three `.env` levels. Three problems
came from this:

- Values that the user exports in the login shell do not reach tools. Under
  launchd or systemd, the process has only `PATH`.
- Users keep credentials in 1Password. They must copy each value into a file.
- A change applies only after the user presses Reload, once per bot.

dex must not become a secret manager. It does not parse `op://` references
or store secret values other than the `.env` files and the 1Password
credentials.

## Decision

### Environment order

The environment of an agent is built in this order. A later source wins per key:

1. The login shell environment. If it cannot be captured, the six process keys.
2. Home `.env`, then dex `.env`, then agent `.env`. The `.env` syntax of ADR
   0010 does not change.
3. The 1Password credentials of the settings, if 1Password is on.
4. For stdio MCP servers, the server's own `env` in `mcp.json`.

### Login shell

dex runs `<shell> -l -i -c` with a command that prints a marker and then
`env -0`. The shell is `$SHELL`, else the shell of the user account (`dscl`
on macOS, `getent passwd` on Linux), else `/bin/sh`. The shell starts from
the six process keys, not from the full dex process environment, so a dex
setting such as `DEX_TOKEN` does not reach tools. Stdin is `/dev/null`, and
the shell runs in its own session. After 10 seconds dex kills the group. Output before the marker is
ignored, so a greeting in an rc file does no harm.

One capture serves all agents. dex captures again only when a common shell rc
file changes (`.zshenv`, `.zprofile`, `.zshrc`, `.zlogin`,
`.bash_profile`, `.bash_login`, `.bashrc`, `.profile`, and
`.config/fish/config.fish` in the home directory). A file that an rc file
sources is not watched; touch an rc file to apply it. A Reload does not
capture again, so a `.env` save does not run the shell once per agent. If a
capture fails, tools keep the last captured environment, or get the six
process keys if no capture succeeded, and the Extensions API shows a warning.

All variables pass to tools without a filter. The user decided this: the
shell environment is already the user's own tool environment. The model can
run `env`, so a token in the shell is visible to every bot, including bots
that a channel reaches. The memory repository keeps the six process keys and
drops `GIT_` keys.

### 1Password

Global settings in `control.sqlite` select one mode: off, Connect (host and
token), or service account (token). Tokens are write-only, like provider API
keys. Mode off keeps the saved tokens.

For Connect, dex sets `OP_CONNECT_HOST` and `OP_CONNECT_TOKEN` and removes
`OP_SERVICE_ACCOUNT_TOKEN`. For a service account, dex sets
`OP_SERVICE_ACCOUNT_TOKEN` and removes the Connect variables, because `op`
prefers Connect when both are present. Off leaves the environment as it is.

dex looks for an `op` executable in the `PATH` of the agent environment. When
1Password is on and `op` exists, the system prompt tells the agent to read a
secret with `op read` inside the command that needs it, to resolve an
environment value that starts with `op://` with `op read "$KEY"`, and to run
a stdio MCP server through `op run --`. When `op` is missing, the prompt does
not mention 1Password, and the Settings and Extensions pages show a warning.

dex never runs `op` itself and never sees a secret from 1Password.

### Automatic reload

A scheduler job runs every 3 seconds. For each open agent, it compares a
stamp with the stamp from the last load. The stamp holds the size and
modification time of the `.env` and `mcp.json` files of all levels and of the
shell rc files, and a counter of 1Password changes. When the stamps differ,
the job calls the same Reload as the API. Reload keeps the global idle
precondition of ADR 0012, so a busy engine makes the job try again later. An
invalid configuration keeps the loaded resources and is not tried again until
the stamp changes. The Extensions API reports `reloadPending` until the reload
succeeds. The web UI shows that state instead of Reload buttons. The Reload
API stays.

Polling keeps one mechanism for page saves and hand edits. Use file events if
3 seconds is too slow.

## Consequences

- The model can read every login shell variable and the 1Password
  credentials through `env`. Use a service account or Connect token that can
  read only a vault for agents.
- An agent opens up to 10 seconds slower when the shell is slow.
- A change applies when all agents are idle. A long task delays it.
