# Product

This document is for anyone who decides what dex does. It states the users,
the problem, the scope, and the non-goals.

## Users and problem

dex is for one person who wants personal AI agents that they
can reach from chat apps and a browser, and that keep working over long
tasks. Hosted agent platforms keep the data elsewhere and are hard to change.
dex runs on one machine, keeps all data in local files, and stays small enough
to read in an afternoon.

## Scope

- One `dex` binary. The host needs no other runtime. Access dex through a
  browser or a channel.
- Runtime: foreground execution, or a per-user launchd/systemd service that
  starts at login. Optional daily updates wait until agent work is idle.
  Loopback access needs no token by default. Other listening addresses require
  an access token. See [network access](../README.md#access-from-other-devices).
- Bot settings in the web UI: name, model, thinking level, instructions,
  channels, and routines with their runs. The name has 1 to 40 characters
  after spaces are trimmed. Providers, Secrets, and About are global pages.
  Tool secrets can be set for the current agent or for all dex agents.
  The CLI manages the service address, data directory, and update policy.
- Several agents for one owner, each with a name, model, thinking level,
  instructions, conversations, automations, and workspace directory. The API
  can create, list, read, and update agents. Each agent's API paths include its
  ID, including default. Old API aliases return 404. Global settings contain no
  implicit agent. A resource from another agent returns 404. The web sidebar lists agents
  with default first. Its `+` dialog creates an agent with a name and model,
  then opens its empty timeline. Providers stay global. Each agent has its own channels.
- Durable conversations: control stores each message before delivery. Saved
  messages, reports, and execution records survive a restart. A restart does
  not attach an old CLI process or prove that its actions occurred once.
- One main timeline per agent. The web opens it at `/a/{agentId}` and uses the agent's
  model settings by default. A saved Main override also applies to channel input.
  `/clear` starts a new model context when the timeline is idle. Text after
  the command is carried into the new context. Earlier messages stay readable
  with Show earlier messages. A clear keeps long-term memory. `/compact`
  summarizes older messages, also during a run. A summary made during a run
  joins the context at the run's next step; Stop drops it, and the transcript
  says to run `/compact` again. After a summary, Show earlier
  messages can load the earlier transcript. A clear and a summary show a
  divider. The bot is one page
  with exactly three tabs: Chat, Routines, and Settings. Its
  Settings tab holds General, Instructions, Channels, and Extensions. Its header shows the
  name and the tabs. The model and the thinking level change in Settings. On the
  Chat tab, the header has Chat settings and a Sessions button. Chat settings
  shows a row with the project, model and thinking level of the open chat; a dot
  on the button tells that the chat has its own values. The Sessions button
  shows how many items need the user and how many tasks run. It opens the
  Sessions panel: Needs you, Running, Chats, and Completed. The Chats section
  has New chat. Main comes first and cannot be archived. Chat sessions follow in
  date groups by latest activity: Today, Yesterday, Previous 7 days, Previous
  30 days, then one group per month. Archived chats come last. Only Today
  starts open, and each group shows five chats until the user asks for more.
  The row menu and a right-click archive or restore a Chat. Archiving keeps the
  messages, records and files; a task result still returns to the chat.
  New chat does not reuse an archived
  empty chat. The Sessions panel starts closed, and the browser keeps the user's choice.
  The left sidebar lists only the bots. The user talks to a bot, and the bot
  manages its sessions. Each bot row shows at most one status dot: orange for
  Needs you, blue for Working. Needs you wins. The sidebar also links to global
  settings: Providers, Secrets, and About. On a phone, the menu button shows an
  orange dot when another bot needs the user.
  The browser keeps the sidebar's open or collapsed state. Each agent keeps its unsent main message, uploaded
  attachments, and instructions draft when the user switches agents. Old page
  URLs open the default agent. `/needs-you`, `/work`, and `/inbox` open the
  default bot. `/automations` opens its Routines tab. An unknown agent shows a not-found page.
  Every agent has channel setup controls.
  Channel input shares main. Automation runs have separate read-only transcripts.
  Each run requires an explicit changed, unchanged or failed report. Missing
  reports and execution errors fail the run. Run records include a summary
  and an optional change key; repeated keys mark duplicate reports.
- Main answers short requests directly. It sends longer work to background
  execution and replies at once, so the user can keep writing while the work
  runs.
- Each Chat and automation session runs one main agent loop that does the work itself.
  Skills, tools and the project directory provide capabilities.
  Internal background execution is limited to independent parallel work,
  long commands whose output would flood the context, and independent review.
  Each execution has one input and fixed model settings. The main agent
  handles implementation, review findings, tests, and the final answer.
  Each task starts as soon as it is created. There is no limit on the number of tasks, and tasks in one directory run at the same time. Cancellation withdraws queued work or requests cancellation of running work, and a tool may still complete actions. Background execution cannot delegate more work. The main agent handles failures and decides the
  next action.
- Telegram setup: save a bot token, then open a single-use pairing link to
  allow the owner's account while the bot has no owner. Links expire after
  10 minutes. A paired bot cannot create another pairing link. Manual user-ID
  entry is an advanced option. The bot ignores other users and group chats.
  Earlier versions accepted group messages from allowed users. The bot now
  ignores all non-private chats, including messages from allowed users.
- Entry points: the web UI, Telegram, WeChat, and Feishu / Lark. All channels
  are for the owner. Each bot belongs to one agent. The web selects an agent.
  Web input and channel input for that agent share its main context. Input from one entry can
  affect an answer at another entry. Channel messages show their channel name
  in the web timeline. Replies return only to the input's origin. Web input
  sends no channel reply. Each input gets its own answer, in admission order.
  `/new` clears main's context when main is idle. A repeated command cannot
  clear again. A busy refusal leaves the context unchanged.
  `/stop` stops main's current answer, pending input and background work from
  every main entry. Chat and automation sessions stay outside that scope.
  `/help` lists commands.
  Reply delivery resumes after a restart and skips confirmed
  parts. A crash after remote success can duplicate one part. Bot settings
  show each channel's runtime status and setup.
- Agents can deliver files from main, Chat, Routine runs, and background tasks.
  Dex stores each delivery as an immutable snapshot of at most 20 MiB. The user
  can preview Markdown and safe raster images in Dex and download other files.
  Task deliveries appear in the task record and the parent conversation.
  Continue editing adds the snapshot ID, name, and source to a writable Chat.
  A new delivery is required to publish later changes.
- In main and Chat sessions, the user can attach files and images (at most 10 per
  message, 20 MB each). The agent reads them with its tools. Telegram
  input uses Main's saved settings. Automations default to the agent's settings.
  Telegram accepts photos and
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
  failures recover without a settings change. Bot settings show runtime
  status. Saved Lark settings keep the Lark platform. Groups, threads and
  outbound media are not supported.
- Timers: scheduled messages to an agent. In main or a writable Chat, the
  owner can ask the agent to list, create, inspect, update, pause, resume, or
  run a routine. The agent can read the 10 most recent run results and their
  transcript references. These tools use the current bot; they cannot manage another bot's
  routines. The tools require a user-input turn. Automation runs, background
  tasks, dreams, and task-result continuations cannot use them.
  A partial update keeps fields that the owner did not change. Creation uses
  the current project directory unless the owner selects another directory.
  Replaying one tool call does not create another routine, start another run,
  or move the schedule again. There is no routine deletion tool.

Inputs store their origin. Web input uses `web`. Channel input uses the exact
peer, such as `telegram:7`. A background result keeps the origin of its source
input, also when that source is another result. Automation, internal execution,
and memory consolidation use `automation`, `task`, and `dream`. Channel delivery
uses origin, including for results that finish after a newer input from another
entry. Channel input cannot reach a separate Chat session. Old per-peer
conversations receive no new channel input. Their saved transcript URLs remain readable.

The main chat shows the user's messages and the main agent's answers.
The thoughts and tool calls between two answers show as one collapsed row. While
a tool runs, the row shows its command or path. The row opens by itself when a
call fails. A channel message shows its channel and time below it. The composer
does not repeat the sandbox warning; the empty chat shows it, and screen readers
hear it with the composer.
The web chat marks a delegation with the work's title. A returned result has
a named label and a thin line that groups its replies and tool calls. The mark
shows where the report belongs; it does not confirm that the work reached its goal.
The chat does not show the internal report as input. Read-only task records
give the human the saved report, status, times, and work transcript.
The bot has no Sessions tab. The Sessions panel lists writable Chats and
links to All sessions, the full session list of the bot. On a phone, the
Sessions panel opens as a full-screen sheet.
Routine run history links to read-only automation transcripts.
Old channel transcript URLs remain readable. The legacy Sessions URL remains
available for saved links.
Named dispatch and result labels open the task by its ID.
Done means that the task returned a report. It does not prove goal completion.
The user can request cancellation and mark a finished task handled. The UI does
not show progress percentages.

`New chat` in the Sessions panel opens a writable Chat
with its own model context. It reuses the
newest empty Chat, so an abandoned click leaves no list row. The first message
sets the title. Chat defaults to the bot's model settings and uses its instructions and shared memory.
A saved session model or thinking level override applies from the next input.
Chat does not read the main timeline's context. A task started in Chat returns
its result to that Chat. `/clear`, `/compact`, Stop and earlier messages work within
the selected session. Other sessions cannot enable its Stop.
Each finished answer shows Copy and, when it waits for no tool result, Fork
from here. Each sent message shows Copy and Edit in new chat, which opens a fork
that ends before the message and puts the message text, without attachments,
in the composer. A fork is a new Chat with the source's model, thinking level
and project. It does not run until the user sends a message, and it names its
source. The source stays unchanged, and files on disk are not rolled back. Task conversations are
read-only while queued or running. After the task ends, the user can send messages
and change the conversation's model and project. The original task record, report,
and parent result stay unchanged. Automation transcripts stay read-only.
Main stays the default view. A Chat session opens
under the Chat tab. Task pages open under Chat. Automation transcripts open under Routines.

A bot needs the user when it has an open question, an unhandled failed or
blocked task, a failed routine run that is not handled, or a routine that needs
a model. The Needs you section of the Sessions panel lists each item and opens
the place to act on it. Mark all handled in that section marks every
finished task that needs the user as handled. There is no cross-bot list.
Routine result records include settled `changed` or `failed` outcomes without a
duplicate mark. Unknown and `unchanged` outcomes stay in run history. Opening
a result marks it read. Marking it handled dismisses it. Both states survive restart.

Result details show the outcome, routine, bot, time, summary, and run transcript.
Continue in chat opens a writable Chat for that bot, sets the routine's current
project directory, and sends a first message that quotes the stored result
and exact run reference. Routine history shows outcome dots, summaries,
duplicate marks, and links to saved results. The Routines
tab shows a red dot when an enabled routine's latest run failed.

When the main agent starts background work, it continues independent local
work within the same scope. When no local work remains, it names the background
work in one brief reply and returns without polling for the result. When a report returns,
the reply first names that work and gives its outcome. If work failed or
remains incomplete, the reply states that outcome, the blocker, and what
remains. If the user has changed topics, the reply gives a brief update about
the earlier work. Channel replies also name the work because channels do not
show the web result group.
The main agent checks saved execution records before it reports progress or
starts follow-up work. The built-in coding skill requires a repository path,
project rules, independent review, tests of the final revision, and evidence.
Writes to one repository must run in order or in separate worktrees.

A queued message in the web shows Edit and Remove until its run starts. Remove
withdraws it. Edit withdraws it and puts the text and files back in the box.
A message that already started cannot be withdrawn, and the web says so.

Main and Chat sessions have the `ask_user` tool. The agent asks one question
with 2 to 4 options and the turn ends. The web shows a question card above the
composer. The user picks an option or writes another answer. A plain message
does not answer the question, and the question stays open. Stop, Clear and
archive dismiss it. A Telegram or Feishu user sees the question as a message and
answers it by quoting that message. A plain message does not answer it. WeChat
cannot quote, so a WeChat user answers in the web. Tasks, automations and dreams
cannot ask. The web shows an answer as the question in muted text above the answer.

Web Stop stays in the composer for queued input, an active answer, and active
background work. Stop covers the selected writable session and its background
work. Queued and running task conversations and Automation transcripts are
read-only. Stop needs no model.
Stop withdraws pending input and results, records cancellation, and requests
SDK cancellation once. The reply confirms the request. It does not confirm
process exit. Processes and remote work can continue. Prior actions are not
undone.

Normal completion and execution failure return a report to the main agent.
Stop and cancellation do not create a result turn. A restart interruption
creates one durable result that warns that side effects may exist.
A saved report stays unchanged after cancellation or a late execution event.
The system does not retry failed execution or result delivery. The main agent
reads saved records and artifacts, then decides whether to retry or continue
from saved work. After a user Stop, the main agent waits for new user
authorization. A later user message does not retry an old result. Result
delivery keeps the source input's origin after `/new`.

After a dex restart, running execution becomes interrupted and does not restart
automatically. Queued execution that never started can start for the first
time. Cancelled execution stays cancelled. A dex restart does not prove that old
tool processes ended. Before another execution, the main agent must check prior
artifacts and side effects. Tools run on the host without a sandbox. The SDK
requests cancellation of local commands and MCP calls. It does not guarantee
that detached processes or remote jobs stop. A completed MCP call can return
its report. CLI tools must be logged in and run in the foreground without user
input.

Status: the web UI, Telegram, WeChat, Feishu / Lark, durable messages,
background tasks, timers, skills, and MCP servers are available.
The API, web UI, and channels support several agents. One bot credential can
belong to only one agent. A conflict names the agent that already uses it. Project bindings, task routing between agents, group chats,
and task-specific automation result routing are later work. Other chat channels are not available.

## Non-goals

- Native desktop applications. Use a browser to access the web UI.
- More than one host. One machine runs one dex process.
- Sandboxes. Agent tools run on the host, inside the selected project directory or the agent workspace.
- Shared object storage. Agent files are local directories.
- Automatic failover or data replication.

## Extension files

Each agent reads skills, `mcp.json`, and `.env` from three `.agents/`
directories: the user home, the resolved dex data directory, and the agent
home (`<data dir>/agents/<id>`). The narrower level wins. Tools start
in the agent workspace by default. Main and Chat sessions can select an existing
project directory in the header or with `set_project`. An ended task conversation
can also change its project through the API. The header picker
browses directories on the dex host and offers up to eight recent projects
for the current agent. The directory applies
from the next message after an API change. A tool change also applies to
later tools in the same run. Clear the project to return to the agent
workspace. An automation can set its project directory in its dialog. The
directory applies on every run, including a reused session. Channel input uses
Main's project. Tasks inherit their parent session's directory. A running task
and a dream cannot set a project. Project skills come from
`<cwd>/.agents/skills/`. Memory stays in `<data dir>/memory`.

The system prompt always includes a guide to these files, even with no
skills. It explains paths, file formats, environment use, and Reload. It
contains no environment key names or values. The user sets values on the
Secrets page.

The Secrets page writes tool credentials to the current agent or dex `.env`
file. Dex scope applies to all agents. The page shows key names and shadowed
state, with Replace and Delete actions. Values are write-only. Home keys are
read-only. File changes apply after Reload on the Extensions page or the Secrets
page. The files remain the only store.

The Extensions page in bot Settings shows the level directories, skills, MCP servers, and
configuration warnings. Environment key names are on the Secrets page. Reload
requires all agents to be idle before it replaces the selected agent's execution
resources and MCP connections. After that idle check succeeds, other agents
can start work while Reload continues. Invalid configuration for the selected
agent prevents replacement and keeps its current
resources in place.
Keep `.env` files out of Git and set mode `0600`. The API hides values, but
the model can read files and run `env`. Tool output becomes part of the transcript.
See [extension architecture](dev/architecture.md#extensions) for file loading
and resource lifetime.
