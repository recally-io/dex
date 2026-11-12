# 0017: Drop the home level and add AGENTS.md instructions

Status: Accepted (2026-11-12). Amends [ADR 0010](0010-extension-levels.md).

## Context

A bot has instructions in `control.sqlite`. Users also keep rules in
`AGENTS.md` files that other coding agents read. The home `.agents/` directory
holds personal rules, skills, servers, and secrets that some bots must not get.
ADR 0010 gave every bot the home level.

## Decision

dex does not read `<home>/.agents` at all: no skills, `mcp.json`, `.env`, or
`AGENTS.md`. The levels are:

- Dex: `<data dir>/.agents/`, shared by all agents.
- Agent: `<data dir>/agents/<id>/.agents/`.
- Project: `<cwd>`, for `skills/` under `.agents/` and for `AGENTS.md`.

Skills come from dex, agent, and project. `mcp.json` and `.env` come from dex
and agent. The extensions API and the extension guide no longer list a home
level, and the API scopes are `dex` and `agent` (skills also `project`). dex
gives no warning about files in the home directory. A user who wants a home
file moves it into the dex or agent directory, or links to it with a symlink.
The user home stays known to dex only to expand `~/` in imports.

`AGENTS.md` comes from `<data dir>/.agents/AGENTS.md`, from the agent
`.agents/AGENTS.md`, and from `<cwd>/AGENTS.md`. The agent workspace is the
default cwd. dex does not read parent directories. Equal dex and agent
directories give one file. A bot can opt in to personal rules with a symlink to
`~/.agents/AGENTS.md`.

The model instructions are, in this order: the dex `AGENTS.md`, the agent
`AGENTS.md`, the bot's own instructions, the project `AGENTS.md`. The most
specific text comes last. `---` lines separate the parts. A line names the real
path of each file. With no `AGENTS.md`, the instructions are the bot's own
instructions, unchanged.

A token `@<path>` imports a file. It must start a line or follow whitespace,
and it runs to the next whitespace. `~/...`, absolute and relative paths work.
A relative path starts at the directory of the real file, after symlinks. Fenced
code blocks and inline code spans hold no tokens. A token imports only when its
path is an existing regular file; otherwise it stays plain text. The
extensions API adds a warning when the path contains `/` or ends in `.md`, so
`@name` mentions do not warn. Imports can import, up to 5 levels below the
`AGENTS.md`. dex reads each real file once, which also ends cycles. The text of
each imported file follows the file that first imports it, after a line with its
real path. The `@path` text stays in place.

Project imports are confined. The project `AGENTS.md`, and every file that it
imports, must resolve inside the project directory after symlinks. Others are
skipped with a warning. Dex and agent imports have no limit, because the dex
owner writes those files.

dex builds the text at each admission of input, so an edit applies from the
next input of a conversation, without a restart. It does not watch files.
Skills still apply at each model request.

## Consequences

A user who kept skills, servers, or keys in `~/.agents` moves them to the dex
directory, which reaches every bot, or to a bot directory. Nothing warns about
the old files. The model cannot see a change to `AGENTS.md` inside a run that
already started; it sees it at the next input. The extensions API shows
warnings for the dex and agent files and for the agent workspace, not for the
cwd of each conversation. It does not list the loaded files: that needs an API
schema change.
