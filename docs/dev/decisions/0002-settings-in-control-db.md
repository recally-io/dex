# 0002: Keep all settings in control.sqlite, edited in the web UI

Status: Accepted (2026-10-14). Skills and MCP servers come from files; see
[ADR 0008](0008-agent-extensions.md).

## Context

The first version read the model, the provider keys, and Telegram from
environment variables. The user wants to start dex with no configuration and
set everything in the web UI.

## Decision

`control.sqlite` is the only source of settings. The web UI changes them
through the API, and [`settings.Service`](../../../internal/settings/settings.go)
applies each change at once. dex reads no setting from the environment; only
`DEX_DATA_DIR` and `DEX_ADDR` remain, because they locate the database and the
server.

Control gives the providers (built-in ones with a key, and custom ones with
their API and base URL) to the worker with the `configure` method, after
each worker start and after each change. The worker gets no provider
environment variables, so a key in the shell cannot override the settings.

## Consequences

- A setting has one place, and the UI shows the real state.
- `control.sqlite` holds secrets in plain text. Protect the data directory.
- Configuration as code (for example a deploy with only environment variables)
  needs an API call or a new decision.
