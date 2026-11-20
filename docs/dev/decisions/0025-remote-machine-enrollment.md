# 0025: Remote Machine Enrollment

Date: 2026-11-20
Status: Accepted

## Context

Some machines cannot accept SSH: a laptop behind NAT, a VM on a home network, or
a host that must not open an inbound port. ADR 0022 covers execution kinds, but
an outbound machine still needs an address, a transport identity, and an
install path. A reverse proxy, a trusted certificate, a downloaded binary, and a
manual pairing step would ask the user to decide what Dex can decide: which
certificate to trust and how to identify the machine.

## Decision

- **A separate TLS listener.** Dex serves the machine protocol on its own
  socket, because the web UI socket is plain HTTP and because a machine must
  reach Dex directly, without the TLS proxy that may front the web UI. A host
  that a machine can reach opens the listener on the port after the web UI port,
  so no flag is needed; `--machine-listen` overrides the address, and a
  loopback-only Dex keeps it off. Dex creates a self-signed certificate in
  `<data dir>/machine-tls.pem` on the first start.
- **The install command pins the Dex key.** Dex publishes the base64 SHA-256
  digest of its certificate public key, and the command passes it to `curl
  --pinnedpubkey` and to the machine binary. A machine therefore needs no CA
  bundle, no public domain, and no reverse proxy. The pin, not a DNS name, is
  the server identity.
- **The machine key is its credential.** The installer creates an ed25519 key
  pair, presents the certificate in the pairing request, and stores the PEM in
  `/var/lib/dex-agent/device.json`. Dex stores the SHA-256 digest of the
  public key in `runner_pairing` and verifies the certificate on every
  connection. No bearer credential exists, so no credential can leak from a
  command line or a log.
- **One command, one secret.** The install command carries the Dex address,
  the environment ID, the pin, and a single-use code that expires in ten
  minutes. The installer fetches `install-machine.sh` from Dex, checks release
  checksums, creates the `dex-agent` system account, enrolls the certificate,
  and starts the systemd unit. The code passes to the binary on stdin, so it
  stays out of the process list.
- **Names.** The shipped binary, system account, systemd unit, and state
  directory use `dex-agent`. The UI calls the environment **dex runner** and
  the page **Execution environments**, because the machine, not the service
  name, is what the user chooses. The internal Go package and the
  `/api/runner/` route keep the `runner` name: they carry the execution
  session, and a rename would touch the database and the generated API without
  a user-visible gain.
- **Development builds serve their own payload.** A `dev` version tag makes
  the installer fetch the archive from the listener, so a development UI works
  without a published release.

## Consequences

- The machine listener port must be reachable from the machine. A firewall rule
  or a port forward is enough; the web UI needs no proxy change.
- Rotating the Dex certificate changes the pin. Every machine then needs a new
  install command, so the certificate is long-lived (ten years).
- Revocation deletes the stored digest and closes the live session. The machine
  keeps its key, so it must be enrolled again, not paired again.
- Trust depends on the command channel. An attacker who can read the install
  command can enroll a machine within ten minutes once; a used code fails.
  Anyone who can run a command on the machine can read its key.
- The listener cannot verify host names, so it refuses browser origins and
  accepts only certificate-bearing requests on the machine routes.
