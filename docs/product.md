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
  after spaces are trimmed. Providers, Environment, and About are global pages.
  Global pages show no bot. A bot page changes only that bot.
  Tool keys can be set for one bot in its Tools tab, or for all bots on the
  Environment page.
  The CLI manages the service address, data directory, and update policy.
- Several agents for one owner, each with a name, model, thinking level,
  instructions, conversations, automations, and workspace directory. The API
  can create, list, read, and update agents. Each agent's API paths include its
  ID, including default. Old API aliases return 404. Global settings contain no
  implicit agent. A resource from another agent returns 404. The session tools
  are the exception (see "Cross-bot sessions"). The web sidebar lists agents
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
  Settings tab holds General, Instructions, Channels, and Tools. Its header shows the
  name and the tabs. The model and the thinking level change in Settings. On the
  Chat tab, the header has Chat settings and a Sessions button. Chat settings
  shows a row with the project, model and thinking level of the open chat; a dot
  on the button tells that the chat has its own values. The Sessions button
  shows how many items need the user and how many sessions run. It opens the
  Sessions panel: Needs you and the session list. The list has New chat. Main comes first and cannot be archived. Chat sessions follow in
  date groups by latest activity: Today, Yesterday, Previous 7 days, Previous
  30 days, then one group per month. Archived chats come last. Only Today
  starts open, and each group shows five chats until the user asks for more.
  The row menu and a right-click archive or restore a Chat. Archiving keeps the
  messages, records and files; a child session's result still returns to the chat.
  New chat does not reuse an archived
  empty chat. The Sessions panel starts closed, and the browser keeps the user's choice.
  The left sidebar lists only the bots. The user talks to a bot, and the bot
  manages its sessions. Each bot row shows at most one status dot: orange for
  Needs you, blue for Working. Needs you wins. The sidebar also links to global
  settings: Providers, Environment, Archived bots, and About. On a phone, the menu button shows an
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
  Each child session starts as soon as it is created. There is no limit on the number of child sessions, and sessions in one directory run at the same time. Stop on a parent follows the child Stop rules below. A tool may still complete actions. A child session cannot delegate more work. The main agent handles failures and decides the
  next action.
- Telegram setup: save a bot token, then open a single-use pairing link to
  allow the owner's account while the bot has no owner. Links expire after
  10 minutes. A paired bot cannot create another pairing link. Manual user-ID
  entry is an advanced option. The bot ignores other users and group chats.
  Earlier versions accepted group messages from allowed users. The bot now
  ignores all non-private chats, including messages from allowed users.
- Channel pages in bot Settings use one order: Connection, Who can use it,
  Notifications, How to use, and Turn off. A channel that is not connected
  shows short steps and one primary button. Notifications can make the
  channel's owner the primary target for routine results.
- Entry points: the web UI, Telegram, WeChat, and Feishu / Lark. All channels
  are for the owner. Each bot belongs to one agent. The web selects an agent.
  Web input and channel input for that agent share its main context. Input from one entry can
  affect an answer at another entry. Channel messages show their channel name
  in the web timeline. Replies return only to the input's origin. Web input
  sends no channel reply. Each input gets its own answer, in admission order.
  `/clear` clears main's context when main is idle. `/new` is an alias. A
  repeated command cannot clear again. A busy refusal leaves the context unchanged.
  `/stop` stops main's current answer, pending input and background work from
  every main entry. Chat and automation sessions stay outside that scope.
  `/help` lists commands.
  Reply delivery resumes after a restart and skips confirmed
  parts. A crash after remote success can duplicate one part. Bot settings
  show each channel's runtime status and setup.
- Agents can deliver files from main, Chat, Routine runs, and child sessions.
  Dex stores each delivery as an immutable snapshot of at most 20 MiB. The user
  can preview Markdown and safe raster images in Dex and download other files.
  A delivery of a child session appears in that session and in its parent conversation.
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
- Cross-bot sessions ([ADR 0016](dev/decisions/0016-cross-bot-sessions.md)). One
  owner has all the bots, so a bot can give work to another bot. `session_new`
  with `bot` (an ID or a name) creates a child session in that bot. The child
  runs with the instructions, model, memory section, skills, MCP servers, `.env`,
  and workspace of that bot. This gives the caller the tools and credentials of
  that bot, and the owner accepts it. The result comes back to the conversation
  that created the child. Only that conversation can send to, stop, or fork the
  child, whatever bot the child belongs to. A child cannot create children, so
  bots cannot delegate in a loop. Any session of any bot can be read, except
  dreams. `session_search` and `session_list` cover the current bot by default,
  all bots with `all`, or one bot by ID or name, and each result names its bot.
  `session_list` always shows the children of the conversation, whatever their
  bot.
  A file that a child delivers in a turn of its parent shows in the parent
  conversation, also when the child belongs to another bot. Bot
  isolation holds for everything else: the API returns 404 for another bot's
  resources, and channels, routines, and `.env` stay with their bot.

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
The chat does not show the internal report as input. The user opens the
child session to read its full work.
The bot has no Sessions tab. The Sessions panel lists Chats and child sessions,
newest activity first, and links to All sessions, the full session list of the
bot. A child session is a Chat session with a parent. Its row shows "Started by"
and the name of the parent: Main, or the title of a Chat, also when that Chat is
archived. The panel has no task rows, no step preview, no Stop on a row, no Mark
handled and no Completed group. On a phone, the Sessions panel opens as a
full-screen sheet.
A busy session shows a dot in the panel and a Running badge in All sessions. The
dot and the badge change to Stalled when the session shows no sign of work: a
tool has run for more than 30 minutes, or no tool runs and the engine saw no
activity for 5 minutes. The hint checks the time every 15 seconds, so it appears
with no click.
A child session that runs in another bot appears in the parent bot’s panel and
All sessions list. The row names its bot and opens in that bot. The child bot
names the parent bot.
Routine run history links to read-only automation transcripts.
Old channel transcript URLs remain readable. The legacy Sessions URL remains
available for saved links.
Named dispatch and result labels open the child session that holds the turn. An
old `/tasks/<id>` link, with or without the bot prefix, opens the session of that
old task in the same way. The UI has no Task page and no Task kind in the filter.
A result shows that the child returned a report. It does not prove goal
completion. The UI does not show progress percentages.

`New chat` in the Sessions panel opens a writable Chat
with its own model context. It reuses the
newest empty Chat, so an abandoned click leaves no list row. The first message
sets the title. Chat defaults to the bot's model settings and uses its instructions and shared memory.
A saved session model or thinking level override applies from the next input.
Chat does not read the main timeline's context. A child session started in Chat returns
its result to that Chat. `/clear`, `/compact`, Stop and earlier messages work within
the selected session. Other sessions cannot enable its Stop, except that the Stop of a parent covers
the work that the parent sent to a child, as the Stop rule below says.
Each finished answer shows Copy and, when it waits for no tool result, Fork
from here. Each sent message shows Copy and Edit in new chat, which opens a fork
that ends before the message and puts the message text, without attachments,
in the composer. A fork is a new Chat with the source's model, thinking level
and project. It does not run until the user sends a message, and it names its
source. The source stays unchanged, and files on disk are not rolled back. A child
session is a normal chat page with the normal composer and a link to its parent.
The user can send messages to it at any time, also while it runs: Enter steers
the run, like in any Chat. The user can change its model and project. Its result
in the parent stays unchanged. Automation transcripts stay read-only.
Main stays the default view. A Chat session or a child session opens
under the Chat tab. Automation transcripts open under Routines.

A bot needs the user when it has an open question, an unhandled failed or
failed routine run that is not handled, or a routine that needs a model. The
Needs you section of the Sessions panel lists each item and opens the place to
act on it. A failed child session goes to its parent, which tells the user; it
is not a Needs you item. Mark handled applies to a failed routine run only.
There is no cross-bot list.
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
background work. Stop covers the selected writable session. On a parent, it
also covers each child where it can stop work that the parent sent. The session
list marks those children (`stoppedWithParent`), and the parent page shows Stop
for them. A child that also has pending user input keeps its run, and
Stop withdraws only parent input that is still queued or not admitted. A run that the user
started in a child is not covered: the parent page shows no Stop for it, and the
user stops it in the child. Stop on a child
ends all pending input of that child. Automation transcripts are read-only.
Stop needs no model.
Stop withdraws pending input and results, records cancellation, and requests
SDK cancellation once. The reply confirms the request. It does not confirm
process exit. Processes and remote work can continue. Prior actions are not
undone.

Normal completion and execution failure return a report to the main agent.
A completed run that holds parent input returns its answer once. A failed run
returns one report for each parent input in it. Work cancelled by Stop or
cancellation does not create a result turn. A kept parent-started run can
return a result that starts a new parent turn. A restart interruption creates
one durable result for each parent input that the run held, and the result
warns that side effects may exist.
A saved report stays unchanged after cancellation or a late execution event.
The system does not retry failed execution or result delivery. The main agent
reads saved records and artifacts, then decides whether to retry or continue
from saved work. After a user Stop, the main agent waits for new user
authorization before it resumes cancelled work. A later user message does not retry an old result. Result
delivery keeps the source input's origin after `/clear`.

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

- Bots: in main or a writable Chat, the owner can ask the agent to list bots
  (`bot_list`), read one bot (`bot_get`), create a bot (`bot_create`), change a
  bot's name, instructions, model or thinking level (`bot_update`), and archive a
  bot (`bot_delete`). A change uses the same checks as the web. `bot_create` makes
  a bot with no channels, skills, MCP servers or `.env` values. Reading also works
  in an automation run. Creating, changing and archiving need a user-input turn
  in main or a Chat, so automation runs, child sessions, dreams and task results
  cannot use them. The tools never show `.env` values.
- Archive: `bot_delete` archives; it never deletes data. The tool only asks. The
  web shows an approval card: the first option is `Archive <name>`, the second
  is Cancel. Only an answer of the first option from the web or from a paired
  channel peer archives the bot. Surrounding whitespace is ignored; the words,
  case and inner spaces must match. Any other answer, or an answer from a task,
  automation or dream, leaves the bot as it is. The agent sees the decision
  as the last line of the answer. The default bot, the asking bot and a bot with a
  running session are refused. An archived bot leaves the sidebar and the bot
  list, its Telegram, Feishu and WeChat channels stop, and its routines stop
  running. Its chats, memory, settings and files stay. Settings > Archived bots
  lists archived bots with a Restore button. Restore brings back the sidebar row,
  the channels and the routines as they were. A routine whose time passed while
  the bot was archived runs again only if it is at most one hour late.
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
project directory in the header or with `set_project`. A child session
can also change its project through the API. The header picker
browses directories on the dex host and offers up to eight recent projects
for the current agent. The directory applies
from the next message after an API change. A tool change also applies to
later tools in the same run. Clear the project to return to the agent
workspace. An automation can set its project directory in its dialog. The
directory applies on every run, including a reused session. Channel input uses
Main's project. Child sessions inherit their parent session's directory. A child
session in another bot starts in the workspace of that bot. A dream cannot set a
project. Project skills come from
`<cwd>/.agents/skills/`. Memory stays in `<data dir>/memory`.

The system prompt always includes a guide to these files, even with no
skills. It explains paths, file formats, environment use, and Reload. It
contains no environment key names or values. The user sets values in the web
UI or in the files.

Tools start from the user's login shell environment, so a value that the
shell exports reaches every bot. The Environment page can also store
1Password credentials for the `op` CLI: a service account token, or a
Connect host and token. When `op` is installed, bots read secrets with
`op read` when a command needs them. dex does not read the secrets.

The Tools tab of a bot writes that bot's `.env` file. It shows dex and home
keys as read-only, with shadowed state for this bot. The Environment page
writes the dex `.env` file, which applies to all bots, and shows home keys as
read-only. Both pages show key names with Replace and Delete actions. Values
are write-only. A key whose value looks like a pasted `NAME=...` line, or is
empty, padded, or an `op://` reference, shows a badge, and a save of an
assignment-like value shows a warning; dex still saves it. Changes to files, shell rc files, and 1Password settings apply
automatically when all bots are idle; the pages show a change that waits. The
files remain the only store. The old `/secrets` address
opens the Environment page.

The Tools tab also shows the level directories, skills, MCP servers, and
configuration warnings. The automatic reload
requires all agents to be idle before it replaces the selected agent's execution
resources and MCP connections. After that idle check succeeds, other agents
can start work while Reload continues. Invalid configuration for the selected
agent prevents replacement and keeps its current
resources in place.
Keep `.env` files out of Git and set mode `0600`. The API hides values, but
the model can read files and run `env`. Tool output becomes part of the transcript.
See [extension architecture](dev/architecture.md#extensions) for file loading
and resource lifetime.
