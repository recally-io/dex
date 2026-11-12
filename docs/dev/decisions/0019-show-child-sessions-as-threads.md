# 0019: Show child sessions as threads in the web

Status: Proposed (2026-11-12). Refines the result projection of
[ADR 0015](0015-child-sessions.md). PR #140 holds ADR 0018.

## Context

[ADR 0015](0015-child-sessions.md) (#127) hid child results on purpose. The
projection showed a `task` mark without the answer. Main relayed the outcome,
and `sessionRules` told Main: "The user cannot see this input."

That rule had a gap. A result turn of Main can end with no visible text: the
engine accepts a finished answer with empty text, and the projection shows
nothing for a reply without text blocks. Then the web showed only a mark, and
the user could not read the result at all. PR #141 fixed this by copying the whole child answer
into the mark. That fix has three costs:

- The answer shows twice, once in the mark and once in the reply of Main.
- A long answer fills the main transcript. The child's tool calls and its
  follow-up messages stay hidden.
- The prompt still said that the user cannot see the input, so Main could not
  tell what the user sees.

## Decision

The web shows each child as a thread, like a Slack thread. The main transcript
stays short, and the full child conversation opens in a side panel.

- **Card.** Each `session_new`, `session_fork` and legacy `spawn_task` call
  shows one thread card at its place in the steps. The card has the title, the
  bot name for a child in another bot, the state, the number of turns and the
  first two lines of the latest answer. A fork makes a new child, so it gets its
  own card. A `session_send` shows one line ("Sent to <title>") that opens the
  same thread. Cards and lines stay visible when the other steps collapse.
- **Result line.** A result mark is one line: "<title> replied · View thread". A
  failed or interrupted result shows its status and reason on the line. The
  projection carries the status and the reason, and never the answer text.
- **Panel.** A card, a line or a result opens a read-only panel on the right.
  It reuses `Transcript`, `getChat` and the chat stream, so it is live while
  the child runs. Its header has the title, the bot, the state, a close button
  and a link to the child as a session page. On a narrow screen the panel covers
  the transcript. The open thread is not in the URL. The panel is a plain
  docked element and not a modal Sheet, because the user keeps working in the
  parent.
- **Data.** The web reads the child ID and the bot from the session tool result.
  It finds the child of a result mark and of a legacy task with `getTurnChat`.
  State, turn count and preview come from `getChat` and `getChatHistory`. The
  only API fields that this change keeps are `status` and `reason` on the
  `task` item.
- **Main still relays.** Channels (Telegram, WeChat, Feishu) have no thread and
  show only the reply of Main. `sessionRules` now says that a web user can open
  the child as a thread and read its full answer, and that a channel user sees
  only the reply of Main. Main names the work, gives the outcome and what
  matters, does not copy the whole answer, and ends every result turn with a
  short text reply.

## Consequences

- The main transcript no longer shows the child answer, so the result of #127
  stays compact. The user can read the full answer, and the whole child
  conversation, one click away. This works also when Main gives no visible
  answer.
- Each mounted card polls its child chat every 3 seconds while the child is
  busy and every 30 seconds otherwise. It reads the child history when the chat
  shows new activity. A transcript with many
  cards makes many requests. If this matters, add a list read that returns the
  state and the latest answer of every child of a parent.
- The card state `running`, `idle`, `done`, `failed`, `interrupted` and
  `stopped` comes from the committed history. It can lag the live stream by one
  poll.
- The panel is local page state. A reload closes it. Put it in the URL if users
  need to share a thread.
- Main must still relay, because channels have no thread. A Main that copies the
  whole answer repeats text that the web user can already read, but it costs
  only length.
