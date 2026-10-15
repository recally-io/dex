# Product

This document is for anyone who decides what dex does. It states the users,
the problem, the scope, and the non-goals.

## Users and problem

dex is for one person or a small team who wants personal AI agents that they
can reach from chat apps and a browser, and that keep working over long
tasks. Hosted agent platforms keep the data elsewhere and are hard to change.
dex runs on one machine, keeps all data in local files, and stays small enough
to read in an afternoon.

## Scope

- One `dex` binary. The host needs no other runtime.
- Runtime: foreground execution, or a per-user launchd/systemd service that
  starts at login. Optional daily updates wait until agent work is idle.
  Listening addresses are limited to loopback.
- Agent settings in the web UI: providers, the agent's model, thinking level
  and instructions, Telegram, WeChat, and Feishu / Lark. dex needs no configuration file for agent settings. The CLI manages
  the service address, data directory, and update policy.
  The web UI has these pages: Agent, Providers, Channels, Automations, and
  About. The Extensions and Secrets pages and the Slack
  channel show that dex does not support them yet.
- Agents with a model, instructions, and a workspace directory.
- Durable conversations: a crash or a restart does not lose a message or a
  started run.
- Telegram setup: save a bot token, then open a single-use pairing link to
  allow the owner's account while the bot has no owner. Links expire after
  10 minutes. A paired bot cannot create another pairing link. Manual user-ID
  entry is an advanced option. The bot ignores other users and group chats.
  Earlier versions accepted group messages from allowed users. The bot now
  ignores all non-private chats, including messages from allowed users.
- Entry points: the web UI, Telegram, WeChat, and Feishu / Lark. External channel chats are read-only
  on the web. `/new` starts another chat. `/stop` stops pending work. `/help`
  lists commands. Reply delivery resumes after a restart and skips confirmed
  parts. A crash after remote success can duplicate one part. The Channels
  page shows each bot's runtime status.
- In a web chat, the user can attach files and images (at most 10 per
  message, 20 MB each). The agent reads them with its tools. A web chat can
  also use its own model and thinking level instead of the agent's. Telegram
  chats and automations use the agent's settings. Telegram accepts photos and
  documents up to 20 MB, with caption text. Telegram replies show progress,
  code blocks, and basic text formatting. Final-send failures, including flood
  limits, use durable retries with a delay from 5 to 60 seconds. Polling can
  recover from transient startup failures. Permanently rejected setup or
  attachment replies do not block later messages.
  WeChat accepts images and files with the same limits. It uses voice transcription
  as text and shows a placeholder for videos.
- WeChat setup: scan a QR code and confirm on the phone. Only that owner
  can send private messages to the agent. QR status polling stops when the page
  closes. Remote QR expiry permits at most three replacements per attempt;
  there is no fixed five-minute login deadline. Local activation must succeed
  before the page reports a connection. A failed save can retry the same
  confirmation. A stale login asks for a new scan. Its one-hour API cooldown
  survives a restart; a successful new login clears it. Temporary receive or
  storage failures retry without a settings change. CDN 408 and 429 retain the
  message for retry. A replay does not download admitted attachments again.
- Feishu / Lark: QR app creation or manual credentials with a one-time
  pairing code. Only the owner can use the bot in private messages. Text,
  rich posts (including code blocks), images and files reach the agent.
  Replies use a streamed card. A card content rejection allows plain text
  as a fallback. Temporary failures retry the card. Startup and connection
  failures recover without a settings change. The Channels page shows runtime
  status. Saved Lark settings keep the Lark platform. Groups, threads and
  outbound media are not supported.
- Timers: scheduled messages to an agent.

Status: the web UI, Telegram, WeChat, Feishu / Lark, durable messages, timers, skills,
MCP servers, and the worker are available. Named secrets and other chat
channels are not available.

## Non-goals

- More than one host. One machine runs control and one worker.
- Sandboxes. Agent tools run on the host, inside the agent workspace.
- Shared object storage. Agent files are local directories.
- Automatic failover or data replication.
