# 0007: Network access with an access token

Status: accepted. Date: 2026-10-15. Supersedes the loopback-only rule of
[0006](0006-service-and-update.md).

## Context

The owner wants to use dex from other devices, for example with
`--host 0.0.0.0`. The API runs shell commands through the agent, so a
listener that the network can reach must not accept requests without a
credential. dex serves plain HTTP and has no user accounts.

## Decision

- **Auth applies** when the host is not loopback, or when DEX_TOKEN is set.
  A loopback host without DEX_TOKEN keeps the old behavior: no token.
- **One access token.** DEX_TOKEN, when it is set, is the token. Otherwise dex
  uses the token in `control.sqlite`. dex creates that token with 32 random
  bytes on each start when it is missing, and never replaces it. A token has
  32 to 256 base64url characters. Sign-in has no rate limit, so a user token
  must be random.
- **Every `/api/` request** needs the token, as `Authorization: Bearer` or as
  the `dex_session` cookie, `GET /api/health` included. Only
  `POST /api/session` is public; it exchanges the token for the cookie. The web
  UI files are public, so the browser can show the sign-in form.
- **The cookie** holds the token. It is HttpOnly and SameSite=Strict, and has
  no Domain and no Secure flag. Cross-origin protection stays in front of
  authentication, so another site cannot sign in from the user's browser.
- **The service uses the stored token only.** Its definition passes
  `--stored-token`, so a DEX_TOKEN that the service manager passes on has no
  effect. Definitions still store no credentials.
- **Local clients** (`service status`, `doctor`, the updater) read the stored
  token from `control.sqlite` read-only, without migrations. An older schema
  means no token. `dex token` prints the stored token.

## Consequences

- Plain HTTP exposes the token to anyone who can watch the network. Prefer a
  host address on an encrypted network, such as a Tailscale address, to
  `0.0.0.0` on a shared LAN.
- Rotation, logout, and server-side sessions are deferred. To rotate today,
  the user must delete the row and restart dex. Add a rotate command when a
  token leaks in practice.
- A service definition from v0.1.0 has no `--stored-token`, and an update
  does not rewrite definitions. If the service manager passes DEX_TOKEN, as
  after `launchctl setenv` or `systemctl --user set-environment`, the updated
  service uses it. Run `dex service install` after the update.
- Cookies do not separate ports. Two dex instances on one host name share the
  `dex_session` cookie name, and the last sign-in wins.
- The web UI builds UUIDs from `crypto.getRandomValues`, because
  `crypto.randomUUID` needs a secure context and plain HTTP on a network
  address is not one.
