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
- Internal background delegation for the default agent. Each delegation has
  one input and fixed model settings. The main agent stays available and
  receives reports to plan review, fixes, tests, and the final answer.
  Each agent admits at most 2 queued or running executions. Cancellation
  releases admission, but a tool can still complete actions. Background execution
  cannot delegate more work. The main agent handles failures and decides the
  next action.
- Telegram setup: save a bot token, then open a single-use pairing link to
  allow the owner's account while the bot has no owner. Links expire after
  10 minutes. A paired bot cannot create another pairing link. Manual user-ID
  entry is an advanced option. The bot ignores other users and group chats.
  Earlier versions accepted group messages from allowed users. The bot now
  ignores all non-private chats, including messages from allowed users.
- Entry points: the web UI, Telegram, WeChat, and Feishu / Lark. External channel chats are read-only
  on the web. `/new` starts another chat without cancelling old work.
  `/stop` stops the current answer and background execution for the current
  agent and exact channel peer, including chats before `/new`. Other peers
  and web chats are outside that stop scope. `/help` lists commands.
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

The main chat shows the user's messages and the main agent's answers.
Internal reports, IDs, and execution states have no task page or task cards.
The main agent checks saved execution records before it reports progress or
starts follow-up work. The built-in coding skill requires a repository path,
project rules, independent review, tests of the final revision, and evidence.
Writes to one repository must run in order or in separate worktrees.

Web Stop remains available when the main chat is idle. Stop covers only that
web chat. Channel chats remain read-only on the web. Stop needs no model.
Stop withdraws pending input and results, records cancellation, and requests
SDK cancellation once. The reply confirms the request. It does not confirm
process exit. Processes and remote work can continue. Prior actions are not
undone.

Normal completion and execution failure return a report to the main agent.
Stop, cancellation, and interrupted execution do not start a result turn.
A saved report stays unchanged after cancellation or a late worker event.
The system does not retry failed execution or result delivery. The main agent
reads saved records and artifacts, then decides whether to retry or continue
from saved work. After a user Stop, the main agent waits for new user
authorization. A later user message does not retry an old result. Result
delivery stays in the source chat after `/new`.

After worker loss, running execution becomes interrupted and does not restart
automatically. Queued execution that never started can start for the first
time. Cancelled execution stays cancelled. Worker loss does not prove that old
processes ended. Before another execution, the main agent must check prior
artifacts and side effects. Tools run on the host without a sandbox. The SDK
requests cancellation of local commands and MCP calls. It does not guarantee
that detached processes or remote jobs stop. A completed MCP call can return
its report. CLI tools must be logged in and run in the foreground without user
input.

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
