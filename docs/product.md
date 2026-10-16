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
- Durable conversations: control stores each message before delivery. Saved
  messages, reports, and execution records survive a restart. A restart does
  not attach an old CLI process or prove that its actions occurred once.
- Background tasks for the default agent. The main chat delegates a bounded
  stage with its own model and thinking level, then remains available. Each
  user goal has a short ID and can contain several stages. A completed stage
  leaves the goal open until the main chat records a final report.
  Each agent has two execution slots. Queued, running, cancelling, and unknown
  stages occupy a slot. A stage that needs input ends and releases its slot.
  A later stage can continue the same active goal. Stages cannot create tasks.
- Telegram setup: save a bot token, then open a single-use pairing link to
  allow the owner's account while the bot has no owner. Links expire after
  10 minutes. A paired bot cannot create another pairing link. Manual user-ID
  entry is an advanced option. The bot ignores other users and group chats.
  Earlier versions accepted group messages from allowed users. The bot now
  ignores all non-private chats, including messages from allowed users.
- Entry points: the web UI, Telegram, WeChat, and Feishu / Lark. External channel chats are read-only
  on the web. `/new` starts another chat. `/stop` stops the main answer and
  its queued ordinary messages. Task results remain available. `/tasks` lists
  this chat's open goals and ten recent closed goals. `/task <id>` retrieves
  the saved report. `/cancel <id>` stores a stop request for the whole goal.
  These task commands do not need a model. `/help` lists commands.
  Reply delivery resumes after a restart and skips confirmed
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

The main chat shows task cards, stage models, actual tool events, and full
saved reports. Tool history is read-only. The built-in coding skill defines
implementation, independent review, final-version tests, and an evidence report.
Coding stages must carry the repository path, project rules, and acceptance
criteria. Writes to one repository must run in order or in separate worktrees.
The skill does not enforce a repository lock.

Cancellation accepted means that control saved the request. Only confirmed
execution exit produces `cancelled`. Unknown execution retains its slot and
never restarts automatically. Cancellation does not undo earlier actions.
The first version has no action to clear an unknown slot. A stage that calls
an MCP tool cannot yet confirm that remote work ended, so it remains unknown
even after a report. Loading MCP tools does not prevent local stages from
completing. Tools run on this machine without a sandbox.
CLI tools must be logged in and run in the foreground without user input.

Status: the web UI, Telegram, WeChat, Feishu / Lark, durable messages,
background tasks, timers, skills, MCP servers, and the worker are available.
The current task milestone uses one default agent. Project bindings, task
routing between agents, group chats, and task-specific automation result
routing are later work. Named secrets and other chat channels are not available.

## Non-goals

- More than one host. One machine runs control and one worker.
- Sandboxes. Agent tools run on the host, inside the agent workspace.
- Shared object storage. Agent files are local directories.
- Automatic failover or data replication.
