# 0009: Share channel state and capture the owner during setup

Status: Accepted (2026-10-15).

## Context

External channels use the same agent, tools, memory, and workspace as the web.
Tools run on the host. A channel must admit only its owner or its explicit
allowlist. Messages and replies must survive control and worker restarts.

## Decision

Use a small `internal/channel` package, without a plugin registry. It owns peer
mapping, shared commands, runtime status, and durable reply delivery. Platform
packages own their protocol, inbound conversion, and reply format.

Keep the peer separate from the conversation key. `/new` rotates the key in
the same transaction as its command receipt. Old keys retain their destination.
A stable source key includes the platform, bot identity, and remote message ID.
It does not include the rotating conversation key.

Each platform has one sender. It reads settled submissions and records each
confirmed part. Fixed command replies record only whole-reply delivery.
`Bot.Send` receives the peer, stable delivery ID, part index, and text.
Notify carries only the channel key and wakes the sender. At start, the sender
resumes undelivered replies.
Delivery is at least once. A crash between platform success and the local
commit can duplicate one part. Old settled messages are excluded on upgrade.

Capture the owner during setup when the platform returns an authenticated
user identity. WeChat uses the person who confirms QR login. Other senders
must be ignored. Keep the existing Telegram allowlist until its pairing flow
is available. Missing owner identity must never open access.

Settings and credentials stay in `control.sqlite` and change only through
`internal/settings`. Cursor and context tokens are runtime state. The settings
API must not expose credentials or runtime tokens.

## Consequences

- A platform needs no registry or worker changes.
- External channel chats stay read-only on the web, including old chats.
- Commands and input deduplication survive restarts.
- A confirmed reply part can repeat only in the remote-success/local-commit gap.
- Owner capture removes manual user-ID entry where the platform supports it.
