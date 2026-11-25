# Tasks

This document describes Board, the Task event store, holds, Task
alerts, and the Task tools. Optional sources that send Task events are in
[Plugins](plugins.md).

## Board and event store

Board reads `work_tasks` and `work_task_entries` from `control.sqlite`. Each
Task belongs to a bot. Its lifecycle and notes do not change the state of a
Session. Board hides closed Tasks by default. Inbox shows items that need owner
action, including held events and Automation results. The owner can send a
message from a Task to Main. The
Task link and timeline entry commit with the message receipt. An answer to a
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

A Task alert is a `task_result` submission on the bot's Main. It is linked to
the Task, so Main shows it with the Task title. Dex creates an alert when a
source rule creates a Task, or when an event resolves a wait that has `alert`
set. Dex writes the alert in the same transaction as the event, with the event
ID as its source key, so each event sends at most one alert. A replayed
delivery or a restart does not send a second alert. If the runner is offline,
the alert stays pending until the runner returns. The alert contains only the
event type, author class, source link, and the reason. It does not contain the
event text. An alert is a system input, so it cannot create, complete, cancel,
or reopen a Task. A quiet wait, a record-only event, a closed Task, an
unmatched event, and a link conflict do not send an alert.

Main and writable Chats have the `task_list`, `task_get`, `task_new`,
`task_update`, and `task_append` tools (`internal/worktask/tools`). They record
work and never start, steer, or stop a Session. The tools see only the Tasks of
the caller's bot. `task_new` and a lifecycle change need a run that a person
started on the web or a channel. A session result, a monitor, or an external
event cannot create, complete, cancel, or reopen a Task. Each change commits
with a replay record under the tool key, so a replayed call does not add a
second Task or entry. `task_get` returns the latest 20 entries, cuts each
entry to 2000 characters, and omits the text of external entries. It gives
only their event type, author class, and source link, so text from people
outside the bot does not reach the model. Each entry has an author: `owner`
(a note or a message from Task detail), `agent` (a tool call), or external (a
source event). Entries from before the author column show as unknown.
`task_get` and `GET /api/agents/{agentId}/inbox/tasks/{taskId}` list every
linked object as `links`: its namespace, type, and key, with the waits on that
object. The core does not decode these values, so links stay readable when no
source can read them.

`work_task_sessions` holds the primary work Session of a Task. When the input
that started a Main run is linked to a Task, the first `session_new` of that
run links its child, in the same transaction as the child. A later child does
not replace it. `task_update` with `session` sets any child of the caller.
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
