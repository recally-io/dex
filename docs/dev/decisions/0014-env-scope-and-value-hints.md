# 0014: Scope of .env values and shape hints

Status: Accepted (2026-11-11). Amends [ADR 0013](0013-tool-environment-sources.md).

## Context

A bot on production tried to give `ANTHROPIC_BASE_URL` and
`CLAUDE_CODE_OAUTH_TOKEN` to Claude Code in a terminal app. Two sessions
failed for two reasons:

- The user pasted one line, `ANTHROPIC_BASE_URL=<url> CLAUDE_CODE_OAUTH_TOKEN=<token>`,
  as the value of `CLAUDE_CODE_OAUTH_TOKEN`. dex saved it without a hint. The
  proxy answered 401.
- The bot did not know where `.env` values are visible. They reach only the
  tools that dex starts. A terminal app starts its program from the login
  shell of the user, so that program has none of them. The bot must not read
  values, and dex gave it no other way to diagnose the problem.

## Decision

### Shape hints

dex checks each `.env` value when it scans the files. The check returns hint
names. It never returns value text. The hints are:

- `assignment`: the value has `NAME=` at its start, or after whitespace,
  followed by more text. `NAME` is an upper case variable name, such as
  `ANTHROPIC_BASE_URL`. A base64 padding (`dGVzdA==`, `dGVzdDE=`), a query
  string (`?a=b`), a libpq DSN (`host=a port=1`), and `Server=a;Database=b` do
  not match. The cost is a miss on a pasted lowercase name such as
  `http_proxy=...`. A value such as `HOST=a PORT=1` is a false hint that dex
  accepts.
- `empty`: the value is empty or only whitespace.
- `whitespace`: the value starts or ends with whitespace.
- `op-reference`: the value starts with `op://`.

The hints are heuristics. A false hint costs the user one look; a missed hint
costs a failed session. Add a hint only when it can be checked without the
value leaving the process.

The check runs where `.env` is already parsed, so one function serves the API
and the prompt. The check does not run on the login shell environment, the
`mcp.json` env, or the 1Password credentials.

### Provenance

Each entry of `env` in the Extensions API gets a `hints` array next to `key`,
`scope`, and `shadowed`. The Environment page and the Tools tab show a badge
for each hint. After a save, the response carries the hints of the saved key.
The pages warn when it has `assignment`. dex never refuses a save: a value
that looks like an assignment can be correct, and the user decides. The pages
do not repeat the pattern in TypeScript. The server owns the check.

### Scope in the system prompt

The extension guide says:

- `.env` values and the 1Password credentials exist only in tools that dex
  starts. A program that the bot starts outside dex (a terminal app, a GUI
  app, another session) gets none of them. A terminal app starts from the
  user's login shell, so it has only what the shell rc files export. This
  lets the bot tell a key in a shell rc file from a key in `.env`.
- To cross that boundary, pass the value in the command for the target, for
  example `env KEY="$KEY" cmd`. When `op` is available, run `op read` in the
  target, so the target resolves the reference. Never print a value.
- The guide lists each key that has a problem hint (`assignment`, `empty`,
  `whitespace`), with a plain sentence for each hint. It does not list
  `op-reference`: that value is valid, and the 1Password text of ADR 0013
  already explains it. It skips shadowed keys, because their values are not in
  use. It lists at most 20 keys and then gives a count. The guide never lists
  a key without a problem hint, and never lists login shell variables.

The guide is built at load time. The automatic reload of ADR 0013 refreshes it
after a `.env` change.

## Consequences

- The prompt now holds key names, but only names of keys with a problem. This
  changes the old promise that the guide has no key names. A key name is not a
  secret in dex (the API already lists them).
- A key with a hint costs prompt tokens on every request until the user fixes
  it. The limit of 20 keys bounds this.
- A bot can say "the value of `CLAUDE_CODE_OAUTH_TOKEN` looks like a pasted
  assignment" without reading the value.
- The `op-reference` hint is information, not an error. The API and the pages
  show it, so the user can see that a program outside dex receives the literal
  `op://` text.
