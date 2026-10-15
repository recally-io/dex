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
  and instructions, Telegram, and Feishu / Lark. dex needs no configuration file for agent settings. The CLI manages
  the service address, data directory, and update policy.
  The web UI has these pages: Agent, Providers, Channels, Automations, and
  About. The Extensions and Secrets pages and the Slack and WeChat
  channels show that dex does not support them yet.
- Agents with a model, instructions, and a workspace directory.
- Durable conversations: a crash or a restart does not lose a message or a
  started run.
- Entry points: the web UI, Telegram, and Feishu / Lark. External channel chats are read-only
  on the web. `/new` starts another chat. `/stop` stops pending work. `/help`
  lists commands. Reply delivery resumes after a restart and skips confirmed
  parts. A crash after remote success can duplicate one part. The Channels
  page shows each bot's runtime status.
- In a web chat, the user can attach files and images (at most 10 per
  message, 20 MB each). The agent reads them with its tools. A web chat can
  also use its own model and thinking level instead of the agent's. Telegram
  chats and automations use the agent's settings and take no files.
- Feishu / Lark: QR app creation or manual credentials with a one-time
  pairing code. Only the owner can use the bot in private messages. Text,
  rich posts (including code blocks), images and files reach the agent.
  Replies use a streamed card. A card content rejection allows plain text
  as a fallback. Temporary failures retry the card. Startup and connection
  failures recover without a settings change. The Channels page shows runtime
  status. Saved Lark settings keep the Lark platform. Groups, threads and
  outbound media are not supported.
- Timers: scheduled messages to an agent.

Status: the web UI, Telegram, Feishu / Lark, durable messages, timers, skills,
MCP servers, and the worker are available. Named secrets and other chat
channels are not available.

## Non-goals

- More than one host. One machine runs control and one worker.
- Sandboxes. Agent tools run on the host, inside the agent workspace.
- Shared object storage. Agent files are local directories.
- Automatic failover or data replication.
