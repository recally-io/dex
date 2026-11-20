# 0025: Remote Machine Enrollment

Date: 2026-11-20
Status: Accepted

## Context

Some machines cannot accept SSH: a laptop behind NAT, a VM on a home network, or
a host that must not open an inbound port. ADR 0022 covers execution kinds, but
an outbound machine still needs an address, an identity, and an install path. A
downloaded binary and a manual pairing step would ask the user to decide what
Dex can decide: which certificate to trust and how to identify the machine.

## Decision

- **One listener, behind the operator's HTTPS front.** The machine routes sit
  on the same socket as the web UI, so Dex terminates no TLS and needs no
  certificate of its own. A machine reaches Dex at the address of the web UI,
  which is an ordinary HTTPS origin: nginx, Caddy, Tailscale Serve, or a cloud
  load balancer terminate it with the certificate that they already serve. The
  proxy needs the WebSocket upgrade and nothing else. A separate machine
  listener with its own self-signed certificate and a pinned public key was the
  earlier design; it forced the operator to learn which address and which key
  the machine should trust, and it broke on any TLS-terminating proxy, because
  a client certificate cannot reach Dex through one.
- **The machine token is its credential.** Enrollment trades the one-time code
  for a random token and returns it in the response body. Dex stores the
  SHA-256 digest in `runner_pairing` and compares it in constant time on every
  connection. The token goes to `/var/lib/dex-agent/device.json` with mode
  `0600`, and the machine sends it in the `Authorization` header, so a proxy
  log does not contain it. The cost is a bearer credential that the machine
  holds for its lifetime and sends once per connection. It is revocable, and
  copying it needs root on the machine.
- **One RPC per operation.** The session is a JSON-RPC 2.0 connection inside
  the WebSocket. `sourcegraph/jsonrpc2` owns call correlation, reply
  routing, and session loss; Dex owns one method per operation (`exec`, `read`,
  `write`, and the other file operations) and one `cancel` notification. A
  hand-written message protocol would re-implement the parts that are hardest to
  review and easiest to get wrong.
- **One command, one secret.** The install command carries the HTTPS origin,
  the environment ID, and a single-use code that expires in ten minutes. The
  installer fetches `install-machine.sh` from Dex, checks release checksums,
  creates the `dex-agent` system account, exchanges the code for the token, and
  starts the systemd unit. The code passes to the binary on stdin, so it stays
  out of the process list.
- **Names.** The shipped binary, system account, systemd unit, and state
  directory use `dex-agent`. The UI calls the environment **dex runner** and
  the page **Execution environments**, because the machine, not the service
  name, is what the user chooses. The internal Go package and the
  `/api/runner/` route keep the `runner` name: they carry the execution
  session, and a rename would touch the database and the generated API without
  a user-visible gain.
- **Development builds serve their own payload.** The installer asks Dex for a
  development checksum file first, so a development build hands out a command
  that works without a published release.

## Consequences

- The HTTPS origin must be reachable from the machine, and it must serve the
  installer, so Dex cannot move behind a proxy that blocks `/install-machine.sh`
  or the WebSocket upgrade.
- Dex must not be published over plain HTTP: the machine token travels in a
  header on every connection.
- Revocation deletes the stored digest and closes the live session. The machine
  keeps its token file, so it must be enrolled again.
- A rotated token needs a new install command. Dex does not rotate tokens
  automatically, because a machine that fails to store the new token would drop
  off the network for good.
- Trust depends on the command channel. An attacker who can read the install
  command can enroll a machine within ten minutes once; a used code fails.
  Anyone who can run a command on the machine can read its token.
- The machine routes refuse a request that carries an Origin header, so a page
  in the browser cannot use them.
