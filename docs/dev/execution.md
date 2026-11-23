# Execution

This document describes the native execution boundary, background commands,
and transcript subscriptions and history.

## Native execution boundary

`internal/engine.Agents` owns one concrete execution service. Business code
uses typed Go calls and `Ref{AgentID, ID}`. An execution ID names a PiG
conversation, while a chat ID names a business record in `control.sqlite`.
`conversations.execution_id` connects the two records.

- `Create` creates the execution conversation. `Submit` durably admits an
  input with a stable request ID and returns a `Submission`. `Submission.Wait`
  waits for the outcome. Canceling that wait does not cancel the execution.
- `Abort`, `CheckIdle`, and `Reset` operate on one execution reference. Reset
  uses a stable request ID. `Recover` registers child conversations that must
  be isolated before Harness scheduling resumes.
- `History(ctx, ref, before, limit)` returns typed `History`.
  `Detail(ctx, ref, entryID, itemID)` returns one item with its full output. `Watch(ctx, ref)`
  returns a typed initial `Snapshot` and an `Events` channel. `Watch.Close`
  cancels only that subscription. `Quiesce` and `Resume` control maintenance
  admission. Reload does not use them. `Close` ends resource lifetime.

The engine serializes input, reset, and abort operations per execution. Native
input metadata contains request ID, original content, origin, attachments,
source, and optional turn ID and title of a result. `dex.user` and `dex.task_result` entries
keep display facts with the durable input. `dex.withdrawn` entries preserve
queued inputs that Stop removed. Withdrawn entries contribute no model text.

Session tools call injected typed handlers in `internal/chat`. The engine does
not import the HTTP API package. Frontend `Item`, `Event`, `Retry`, and
`History` types live in the engine and keep the HTTP JSON field names. JSON
encoding occurs at the HTTP boundary.

## Command Monitors

A session uses `monitor_set_command` for long work. The call does not wait for
completion. Use foreground `bash` for work that must finish in the current
turn. Do not detach work with `&` or `nohup`. The `monitors` extension also
provides [schedule Monitors](architecture.md#session-monitors).
[`internal/background`](../../internal/background/background.go) owns the
processes. [`internal/chat/background.go`](../../internal/chat/background.go)
binds each command to its session.

- `monitor_set_command` starts the command and
  returns the ID and the output file path at once. It runs in a new session
  (`Setsid`), so the shell leads its own process group, with the environment of
  the bash tool of the bot. A conversation that has a project uses it as the
  working directory. stdout and stderr go to
  `<data dir>/agents/<id>/background/<background id>.log`.
- Wake `exit` (the default) sends one input when the command ends: the ID, the
  exit status, and the last 20 lines (at most 4 KiB) of the output. A command
  with this mode has a default timeout of 1800 seconds. Set an explicit timeout
  in seconds for longer work. A wait on a condition is a command that loops
  until the condition holds.
- Wake `lines` sends the stdout lines as events. dex reads stdout through a
  pipe, copies it to the output file, and batches the lines of one second into
  one input (at most 50 lines and 8 KiB, with a count of the omitted lines).
  stderr is not an event. The command ends at its exit or its timeout (5
  minutes by default, 30 minutes at most), and the session gets a last input.
  An omitted or null timeout uses the default for either wake mode. An explicit
  timeout replaces the default; the 30-minute maximum applies only to `lines`.
  Previously, an `exit` command without a timeout could run indefinitely.
  New commands that need more than 30 minutes must set a longer timeout.
- Wake `none` sends no wake-up. Its default timeout is 1800 seconds.
- `monitor_list` shows both kinds of Monitor in the calling session.
  `monitor_cancel` sends SIGTERM to a running command's process group and
  SIGKILL after 5 seconds. Cancellation sends no wake-up.
- A command owns its whole process group. When the shell leader exits, dex ends
  the rest of the group (SIGTERM, then SIGKILL after 5 s) before it stores the
  final state. Once that sweep starts, dex sends no other signal to the group, so
  a late SIGKILL cannot hit a reused group ID. A short window remains between
  the end of the last member and the end of the sweep: dex accepts it. A stop
  that arrives during the sweep still sets the reason, so the final state is
  `stopped`. A stop that sent SIGTERM before the leader ended counts toward the
  5 s grace of the sweep.

`background_commands` in `control.sqlite` has one row for each command: the
session, the starting input, the wake mode, the label, the command, the working
directory, the output path, the PID, the process group ID, the time that dex
recorded for the start (not an OS identity), the state, the exit status and the
reason. The state is `starting`, `running`, `exited`, `stopped`, `timed_out` or
`unknown`. The reason is `stop`, `shutdown`, `timeout`, `output_limit`,
`rate_limit` or `start_failed`.

The tool call key is unique. `monitor_set_command` stores the row in state
`starting` before it starts the process, then stores the PID and `running`. A
replay that finds any row returns it and never starts a process, so a crash
between the process start and the row write cannot run a command twice. After
the row is stored, the start checks the session again. If a stop withdrew the
session in between, dex stops the new command at once. The final state and the
end wake-up are one transaction, so an ended row always has its wake-up. A
failed write of a `lines` batch can lose that batch: dex does not retry it. dex
retries the end transaction up to 5 times with backoff (100 ms, doubling), then
logs the failure and leaves the row for the next start to mark `unknown`. If the
process started and its row cannot become `running`, dex ends the process and
keeps the row as `stopped` with reason `start_failed`, so the call key stays
used.

A wake-up is a durable input of kind `task_result` in the owning session, as
the result of a child session is. Its ID is `background:<id>:<n>` or
`background:<id>:end`, so a replay stores it once. It keeps the origin of the
input that started the command. A child command wakes only that child. Tools
that need a person's input reject command wake-ups. An input that arrives
while a turn runs queues like other input. An archived bot gets no wake-up.

Stop, restart and limits:

- A stop of a session ends the process group of each of its commands. A stopped
  command sends no wake-up. A child session that keeps its run after a stop of
  the parent keeps its commands. Archiving a bot ends all its commands.
  Archiving a chat does not.
- A graceful shutdown (`App.Close`) stops the process group of every running
  command (SIGTERM, then SIGKILL after 5 s), stores `stopped` with reason
  `shutdown`, and stores an end wake-up that says dex shut down, so the agent
  can start the command again. This runs before the database closes. dex does
  not restart a command by itself.
- Only a crash leaves a process behind. At the next start every `starting` and
  `running` row becomes `unknown`: dex cannot tell whether the process runs.
  dex does not stop an `unknown` command, because the OS can give the PID to
  another process. The row keeps the PID and the process group ID, so a person
  can check.
- Limits: 8 running commands for each bot, 64 MiB of output for each command,
  and 100 stdout lines in 1 minute for a `lines` command. A command over the
  output or line limit is stopped and the session gets a wake-up with a notice.
  The output limit is soft: dex checks the file size every 250 ms, so a file
  can pass the limit by what the command writes in that time.
- A `lines` batch has at most 50 lines and 8 KiB. dex counts a line over either
  limit as omitted. dex cuts a line longer than 64 KiB and continues with the
  next line.

## Transcript subscriptions and history

The HTTP contract is [`api/openapi.yaml`](../../api/openapi.yaml): schemas
`Item` and `ChatEvent`.

- The [Go projection](../../internal/engine/view.go) renders immutable native
  conversation revisions. Each revision includes entries, the live partial,
  tool state, retry state, and the full Inbox. The renderer also serves history.
- A first snapshot holds the last 20 turns of the active context. A turn
  starts at an input or a context boundary, and its tool steps count once
  because they fold into one row. When those turns hold more than 300 entries,
  the window starts at the history boundary before the last 300 entries.
  `historyBefore` points at the first entry of the window, and older entries
  load as history. The window start stays fixed for one subscription.
- Snapshots, events, and history pages leave out a done tool output or a
  committed thought longer than 2 KiB and set `truncated`. A thought keeps its
  first line. A failed call and a session call keep their output, because the
  web shows them without a click. `GET .../chats/{chatId}/items/{itemId}?entry=`
  returns the full item when the user opens the step.
- A text-only increase sends `append`. Other item changes send `item`.
  Removal, reordering, a committed partial ID change, or a new history boundary
  sends a replacement `snapshot`. Running and retry changes send `run`.
- Each subscriber has its own native watch. Snapshot capture and subscription
  share one revision. A reconnect needs no replay. Closing one browser tab
  cannot stop another tab's subscription.
- Queued user input comes from the native Inbox. Withdrawal writes passive
  `dex.withdrawn` entries that display the input and an aborted error. Origin,
  attachments, and task labels come from durable metadata. The live hub has no
  queued or failed input overlay and makes no label lookups.
- A slow subscriber can buffer at most 256 frontend events. Overflow stops
  the watch so the browser can reconnect. Execution never waits for that reader.
  SSE sends a comment line every 15 s.
- `live.Hub.Changed` only helps a stream attach when an empty business chat
  receives its first execution reference. The hub encodes typed engine events
  as JSON at the SSE boundary.

`POST /api/agents/{agentId}/chats/{id}/messages/{messageId}/withdraw` removes one
queued input. Under the agent delivery lock, `Agents.Withdraw` calls PiG's
`Submission.Abort`. PiG decides: `aborted` means the input never runs, and
`already_placed` means it runs, so dex answers 409. The control receipt settles
as `unanswered` with reason `withdrawn` and keeps its request ID and source
key, so a channel redelivery does not run it. It sets `delivered_at`, so the
reply outbox sends nothing. A repeated call returns the same message. The
withdrawal writes no `dex.withdrawn` entry, so Edit leaves no stale bubble.

`POST /api/agents/{agentId}/chats/{id}/clear` uses the agent delivery lock and a SQLite writer
transaction to check pending input and insert `context_reset` together. The
engine also checks its active run and Inbox. A refused request stores no reset.
`Agents.Reset` submits a passive `pi.reset` entry with a stable request ID and
`head: self`. A retry adds no second reset. A handoff is stored in the receipt
content and becomes the reset entry's user message. It does not start a run;
the next run reads it. Reset entries keep their time in
data. Memory files remain available. Follow-ups keep one input per answer;
they do not merge all queued input or steer the current turn.

The composer runs `/clear [handoff]` (alias `/new`) and `/compact [what to keep]`.
A draft that starts with `/` is never sent as a message. The server alone
decides whether work is active. A refused `/clear` shows a warning and keeps the
same request ID for a retry with the same handoff. The menu greys out `/clear`
and `/compact` when the last non-compaction item is a plain reset or is absent.

`Agents.Compact` starts a native manual compaction. It also runs during a run;
the summary then waits in the inbox for the run's next boundary. Native abort
keeps queued writes for a later run, so `Agents.Abort` withdraws queued
summaries; otherwise one would wait for the next message and block maintenance.
The view shows a `pending` compaction boundary for each live compaction and a
`queued` one for each summary in the inbox. A goroutine waits for the task, and
then for the queued summary. When no summary is placed, it writes a
`dex.compaction` note entry with the outcome `nothing` or `failed`. A summary
that Stop drops, or that a reset makes stale, counts as failed. A restart
before the goroutine ends loses the note, not the compaction.

`Chat.Fork` starts a new Chat from a writable session. `Agents.Fork` checks that
the entry is visible from the source with `Conversation.Entries`, then calls
`Conversation.Fork`. Without `before`, the entry must be an answer without tool
calls, because a copied call would never get its result. With `before`, the
copy ends at the previous visible entry; when there is none, the fork has no
native conversation yet. The control row stores `forked_from` and copies the
model, thinking level and project, so `EnsureEmptyChat` never reuses a fork and
`ListChats` lists it before its first message. A fork copies committed entries
only, so a running source gives no live run, queue or failure. The native fork
keeps the source configuration until the next admission configures it. Each
request creates another fork; the web disables the action while one is pending.
A failure after the native fork leaves an unused native conversation.

A snapshot contains only the active native context. Reset and summary entries
show dividers. A reset divider shows its handoff; a summary divider can expand
the summary. `historyBefore` points
to the head entry. The browser requests
`GET /api/agents/{agentId}/chats/{id}/history?before=<entry ID>&limit=100` on demand. The engine
reads `Conversation.Entries` before that exclusive cursor and returns items
in entry order with `nextBefore`. A page can extend to the previous assistant
or input so a tool call keeps its result. Committed assistant block IDs use
`e<entry ID>:<block index>`. User IDs remain submission IDs; tool IDs remain
call IDs. The browser merges overlaps by ID and keeps queued input last.
Older withdrawn input loads from the same durable entries through history.
