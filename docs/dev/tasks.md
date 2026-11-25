# Tasks

This document describes Board, the Task event store, holds, Task
alerts, and the Task tools. Optional sources that send Task events are in
[Plugins](plugins.md). For the full Task Board design and rollout, see
[Task Board Architecture](task-board.md).

## Board and event store

Board reads `work_tasks` and `work_task_entries` from `control.sqlite`. Each
Task has one Assignee Bot and a stable key such as `T-42`. Its status and
notes do not change the state of a Session. Board shows Tasks in Kanban or
List view. It hides closed Tasks by default. The legacy Inbox Task API maps
`done` to `completed`, `cancelled` to `cancelled`, and all other statuses to
`open`.

The Owner uses `/api/board/tasks` to list and create Tasks. Detail and write
paths accept a Task key or UUID. A PATCH or note POST can include an
`If-Match` revision. A stale revision returns 412 and leaves the Task
unchanged. Approval requires the current `reviewSeq`. A stale approval
returns 409. Reassignment of an open Task wakes the new Assignee. It does
not transfer Sessions or open questions from the old Assignee.

Task mutations update the Task projection and append a ledger entry in the
same write transaction. Each Task has a stable number, a revision, and entry
sequences. A tombstone retracts an entry without deleting it. The legacy
Timeline shows notes, artifacts, and external events that are not retracted.
The service derives blockers from incomplete sibling dependencies, unresolved waits, and open Task questions.
A closed Task has no blockers. The Main queue position includes open
questions that do not belong to a Task. The service does not store `Blocked`
as a status. The optional entry hook runs only after commit. Consumers must
use the durable entry ID to catch up. `GET /api/board/events` starts at the
ledger head unless the client supplies `Last-Event-ID` or `after`. The
stream sends `board_entry` events in ID order with Task IDs and no entry text.
It then sends `board_ready` with the current cursor. Questions and execution
state can change without a ledger write. Such changes can send
`board_refresh` without advancing the cursor. Board refreshes its reads on
these events. The legacy held-events API is unchanged.

Board returns all matching Tasks and the full Owner timeline for each Task.
Add cursor pages when large Boards or timelines make these reads too slow.

The Owner can create a subtask from an open, user-authorized Task.
A subtask inherits the root authorization and keeps its own Assignee and
status. It can depend on another subtask with the same parent. Dex checks
for cycles inside the write transaction. The Owner can add and remove
dependencies in Task detail. Completion wakes open downstream Tasks that
have no remaining blockers. Cancellation and reopening wake open downstream
Tasks with a notice and keep or restore the blocker. Their status is unchanged.

Inbox shows items that need Owner action, including Tasks awaiting Owner
review, held events, and Automation results. Task reviews link to Board detail. The owner can send a
message from a Task to its Assignee Bot's Main. The Task link and timeline entry commit with the message receipt. An answer to a
question inherits the Task of the question's source message. A conflicting
Task selection does not consume the question. A Task question or `notify_user`
message from Main uses the original channel peer. If the input has no channel
peer, Dex uses the bot's primary notification target when one is configured.
Ordinary replies stay on the input's original route. If Main already has a
question, a new ordinary Task question waits in the queue. The Task detail can
answer it before channel delivery. When no question remains active, settlement
promotes the oldest unanswered question from a settled turn and writes its
Outbox entry in the same transaction. Stop and Clear close all such questions.

The Task event store accepts normalized events. It stores receipt keys and
upstream change keys separately. A receipt, its routing result, the timeline
entry, and any wait resolutions commit in one transaction. Object links use a
source namespace, object type, and object key. If links point to different
Tasks, Dex records the conflict without changing a Task. An event resolves a
wait only on an open Task, when the object, event type, time boundary, and any
specified revision match. This store does not call the model.

Dex holds an event for the owner in `work_source_holds` when its links
conflict. It also holds an unmatched event when the source rule sets
`HoldUnmatched` and the author is not record-only. Otherwise Dex records an
unmatched event and ignores it. The hold commits in the same transaction as
the event, so a replay does not hold it again. `GET /api/inbox/held-events`
lists the newest 100 open holds. The owner can attach a held event to an open
Task with `POST /api/inbox/held-events/{eventId}/attach`. The attach adds the
event to the Task timeline and links each event object that has no Task, so
later events of those objects route to the Task. An object linked to another
Task keeps its link. `POST /api/inbox/held-events/{eventId}/dismiss` removes
the event from the Inbox and keeps the record. Neither decision resolves a
wait or sends a Task alert.

## Task alerts and tools

A source rule that creates a Task adds `assigned` to the Assignee Bot's
[Board wake](task-board.md#coalesced-main-wakeups). An event that resolves a
wait with `alert` set adds `gate_unblocked`. The event and the wake commit in
the same transaction. A replay does not add a second wake. If the runner is
offline, the wake stays pending until the runner returns. Main shows a
`Board wake` mark. The submission contains only Task keys, reasons, and an
entry cursor. Dex links the submission to a Task only if all items refer to
that Task and Main belongs to the Assignee Bot.
A quiet wait, a record-only event, an unmatched event, and a link conflict do
not wake Main. Events on closed Tasks do not wake Main.

Tools accept a UUID or a key such as `T-42`. Main and writable top-level
Chats have `task_list`, `task_read`, `task_new`, `task_update`, `task_post`, and
`task_review`. They also have `task_my_workload`, which lists open Tasks for
the Assignee or Reviewer and puts pending reviews first. `task_list` defaults
to open Tasks and returns up to 50 results. A writable, linked child Session
has only `task_read` and `task_post`, scoped to its exact Session links. The
link does not give the child's Main access to the Task.

On user turns and Task wakes, the parent Assignee's Main can use
`task_split` to create a subtask. It can use `task_assign` to delegate a
subtask, and the current Assignee's Main can hand off an open Task. Delegating
to another Bot defaults to review by the delegating Bot if no Reviewer is
set. Self-assigned subtasks and Owner-created subtasks have no Reviewer by
default. Only the Owner can replace an existing Reviewer. Reassignment does
not change any Session's parent.

`task_new` needs a user turn on Main or in a top-level Chat. On a user turn or
Task wake, the Assignee's Main can change status or submit work for review.
To cancel or reopen a Task, use the Owner controls. A top-level Chat can
rename a Task or link its child on a user turn. `task_post` writes notes or
artifacts only. The Bot Reviewer uses `task_review` on Main: approval must
match the current `reviewSeq`; a request for changes needs a note and wakes
the Assignee's Main after commit. Each write stores a replay record with the
change. A replay does not repeat the change or wake.

`task_read` gives Task details and returns entries by sequence. Use explicit
`afterSeq: 0` to start a full scan, then use `nextCursor` for the next page.
The default limit is 20 and the maximum is 100. Without `afterSeq` or tags,
`task_read` selects up to 16 KiB of entries, with `brief` and `decision`
notes first, then recent entries. This selection can omit entries below
`nextCursor`. If `hasMore` is true, start at `afterSeq: 0` for a full scan.
Reads do not change shared state. Owner entry bodies are plain text. Bot and
external agent bodies are enclosed in `<task-entry>` with escaped text.
External entries have metadata only. The model sees a key-based title for a
Task created by a Source until an authorized rename. The Owner can still see
the source title. Retracted entries are omitted. Set `includeUpstream` for a separate 16 KiB selection from the direct parent and direct dependencies. This selection excludes all external entries. It does not grant direct access to those Tasks.

`task_read` and `GET /api/board/tasks/{taskKey}` list every
linked object as `links`: its namespace, type, and key, with the waits on that
object. The core does not decode these values, so links stay readable when no
source can read them.

`task_executions` links a Task to Sessions across Bots with the `dex_session`
kind. When a Task input
starts a child through `session_new` or `session_fork`, Dex saves the link in
the same transaction as the child. The first link is primary. A later link
does not replace it. `task_update` with `session` can add a direct child of
the caller, even if that child belongs to another Bot. Set `primary` to `true`
to promote the child without removing other links. This does not grant tools
access to Tasks of the child's Bot or change alert routing.

`task_read` and Task detail return linked `sessions` in primary-first order.
The Board summary returns `primarySession` when present. The legacy
`session` field stays available on the legacy Task API.
The Phase 1 upgrade copies each link with its original Bot, primary flag,
and timestamp. Downgrading restores these Session links. The old schema
cannot store Board status detail, review versions, or non-Session executions.

`task_link_github_pull` from the GitHub plugin links a PR and can add a review
wait that starts at the call time. Set `alertMain` to `true` to send a Task
alert when the review arrives.

| Data                                   | Owner             | Location                                                                                               |
| -------------------------------------- | ----------------- | ------------------------------------------------------------------------------------------------------ |
| Agents, chats, submissions, settings   | control           | `<data dir>/control.sqlite` ([migrations](../../internal/db/migrations/))                              |
| Transcripts, runs, tool calls, usage   | engine (PiG)      | `<data dir>/agents/<id>/state.sqlite`                                                                  |
| Agent files                            | the agent's tools | `<data dir>/agents/<id>/workspace/` (or remote `pi-env` / `e2b` workspace)                             |
| Bot artifact files                     | disk              | `<data dir>/agents/<id>/artifacts/<name>`                                                              |
| Uploaded files                         | control           | `<data dir>/agents/<id>/workspace/uploads/<upload id>/<name>` (staged to remote workspace when remote) |
| Memory (git repo)                      | control / tools   | `<data dir>/memory/` (`memory_*` tools)                                                                |
| Skills and MCP servers                 | the user's files  | `<data dir>/.agents/`, `<data dir>/agents/<id>/.agents/`                                               |
| Outbox messages and follow-up monitors | control           | `outbox_messages`, `outbox_files`, `monitors` in `control.sqlite`                                      |
