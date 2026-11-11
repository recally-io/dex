# 0015: Replace background tasks with child sessions

Status: Proposed (2026-11-11). Replaces the task delegation model in
[architecture](../architecture.md) ("Control also owns internal delegation
records" and the main agent rules). Resolves #99. Uses the session operations
of #100 and changes one #100 rule: a running child is not read-only.

## Context

A task is one conversation, one delegated input, and one result. After the
result arrives, the main agent cannot send more work to that conversation. The
front desk rules tell the main agent to cancel a task and spawn a new one when
the goal changes. Each new task reads the repository again. On 2026-11-11 the
prod Coder bot used three tasks for one change, and each task started with no
context.

The data model also blocks reuse. The result ID is `task:<id>:result` and the
insert uses `ON CONFLICT DO NOTHING`, so a second result for the same task is
lost without an error. The engine accepts one background root per conversation
(`task_binding_test.go`: "second root accepted"). Task steering (#92) has
receipts but no producer.

Chat sessions already have native operations for the user: steer and queue
(#109), continue (#110), clear and compact (#111), fork (#112), stop,
withdraw, and model, thinking, and project settings. They use the PiG
`harness.Conversation` API. A task conversation is a chat session that the
main agent cannot operate.

PiG has no parent and child link between conversations. Its fork parent only
names the source of the copied history. PiG does not tell one conversation that
a run in another conversation ended.

## Decision

### A child session is a chat session with a parent

Add `conversations.parent_id`. A child session is a conversation with a
parent. It uses the same `chat.Service` operations as a user chat session.
There is no task record and no task status.

- Main, Chat, and automation conversations can create child sessions. A child
  session cannot create child sessions.
- The agent tools act only on the children of the calling conversation in the
  same agent.
- The user can open a child session in the web UI and write to it at any time,
  also while it runs. The composer offers send, steer, queue, and Stop.

### Tool set

`parent_id` selects the tool set. Control sets `Child` on every input to a
conversation with a parent, from any origin, including web input. For a child,
the engine removes the session tools, the work loop rules, and the routine
tools, and adds the child rules. The control handler also rejects session tools
from a child. This replaces the one-root task binding. Old task conversations
get `parent_id` in the migration, so they become children and accept
`session_send`.

### Agent tools

| Tool             | Arguments                                                         | Operation                                                                                           |
| ---------------- | ----------------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| `session_new`    | `title`, `message`, `cwd`, `provider`, `modelId`, `thinkingLevel` | Create the child with its settings, then submit `message`. An empty setting uses the parent value.  |
| `session_send`   | `id`, `message`, `mode` (`queue` or `steer`, default `queue`)     | An idle child starts a run. A busy child adds `steer` to the current run and runs `queue` after it. |
| `session_stop`   | `id`                                                              | The same as user Stop. It withdraws queued input and stops the run. It does not roll back files.    |
| `session_list`   | `scope` (`children` or `all`, default `children`)                 | Sessions newest activity first, at most 20. `all` lists every session of the bot except dreams.     |
| `session_read`   | `id`, `before`, `limit` (default 10); or `id`, `turn`, `offset`   | Turns of any session of the bot: the input message and the final answer. See "Reading and search".  |
| `session_search` | `query`, `session`, `since`, `limit` (default 20)                 | Search inputs and final answers in every session of the bot. See "Reading and search".              |
| `session_fork`   | `id`, `entry` (default latest), `message`                         | Fork the child with `chat.Service.Fork`. The fork has the same parent.                              |

Remove `spawn_task`, `list_tasks`, `get_task`, `cancel_task`,
`acknowledge_task`, and `report_task`. `set_project` stays for the calling
conversation. A child gets its project from `cwd`.

The message carries the goal, the constraints, and the completion criteria.
The child prompt asks for a final answer with these parts: what changed,
commands and results, files, and open items. There is no structured report and
no `goalMet` field. The main agent checks the answer against files and tests.

### Reading and search

`session_read` and `session_search` read the turns of every session of the
same bot: Main, Chats, child sessions, and automation sessions. Dream
conversations are excluded. Another bot's sessions are not visible. Child
sessions can also read and search, so a child can read its parent instead of
waiting for the parent to repeat the context.

A turn is one `submissions` row of kind `message`: its input `content` and its
final `answer`, or the failure reason. Thinking, tool calls, tool output, and
other intermediate steps are not returned, because they do not help the reader
and they cost context. Child results (`task_result` rows) are skipped because
they copy the child's answers. The tools read only `control.sqlite`, not the
native transcript.

- `session_read` returns turns newest first, with turn ID, time, origin, input,
  and answer. Each text is cut at 4,000 characters. `turn` and `offset` read the
  rest of one cut text, as `read_dream_message` does.
- `session_search` matches each word of `query` as a substring of the input or
  the answer, ignoring ASCII case, and all words must match. It returns session
  ID, title, turn ID, time, and 150 characters around the first match. A
  substring scan handles Chinese text, and the default FTS5 tokenizer does not.
  The scan has no index (`control.sqlite` is 3.2 MB in prod on 2026-11-11). Add
  a trigram index when one search takes more than one second.

### Where the task fields go

| `tasks` column                                             | New place                                                                                                 |
| ---------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| `id`                                                       | It is a submission ID. Old links resolve through `submissions`.                                           |
| `parent_conversation_id`, `conversation_id`                | `conversations.parent_id` on the child.                                                                   |
| `source_request_id`                                        | New `submissions.parent_request_id` on each child input that the parent sends.                            |
| `spawn_key`                                                | The child input ID, from the agent ID and the PiG tool task ID. Creation and input share one transaction. |
| `title`                                                    | `conversations.title`.                                                                                    |
| `status`, `report`, times                                  | The child submissions: `status`, `reason`, `answer`, `created_at`, `admitted_at`, `settled_at`.           |
| `constraints_json`, `done_criteria_json`, `modifies_files` | Dropped. The message carries them.                                                                        |
| `structured_report`, acknowledgement                       | Dropped.                                                                                                  |

`parent_request_id` has two jobs. A child input with it is a parent turn. A
child input without it is a user turn. The result of a parent turn copies the
origin of the parent request, so channel routing does not change.

### State

Session state is derived. It is not stored.

- `running`: the child has a pending submission.
- `idle`: the child has no pending submission.

Each turn keeps its outcome in `submissions` (`status` and `reason`), as in
other chat sessions.

| Task status           | Child session                                     |
| --------------------- | ------------------------------------------------- |
| `queued`, `admitting` | Removed. PiG admission and the Inbox queue input. |
| `running`             | `running`                                         |
| `done`, `failed`      | Outcome of the latest turn                        |
| `cancelled`           | Latest turn `unanswered`, reason `stopped`        |
| `interrupted`         | Latest turn `unanswered`, reason `interrupted`    |

`session_list` returns ID, title, model, state, the outcome and end time of the
latest turn, and the first 300 characters of its answer.

### Result delivery

When a parent turn ends as done, failed, or interrupted, control writes one
result submission to the parent in the same transaction as the settlement. The
ID is `result:<child submission id>`. Each turn has its own ID, so a later turn
is not lost, and a replay does not make a copy.

- A steered input joins the current run. That run sends one result, which
  covers the steer.
- A queued input starts its own run and sends its own result.
- A stopped run sends no result. The caller of Stop already knows.
- A user turn sends no result to the parent. The parent sees it in
  `session_list`.
- The payload has the child ID, title, turn, outcome, and the answer cut to
  4,000 characters. A cut answer ends with a pointer to `session_read` with `turn` and `offset`.
- The submission kind stays `task_result` and the native entry kind stays
  `dex.task_result`. A `CHECK` constraint, the channel delivery index, and
  saved transcripts use these names, so a rename costs more than it gives.

### Stop and restart

Stop clears `abort_pending` before the next input, so `session_send` after
`session_stop` starts a new run and gives a result.

Stop on a parent chat reaches each child session that holds pending input that
the parent started. Stop treats a child as a whole, because native abort ends
the run and withdraws every queued input of the conversation.

- A child without pending user input ends all of its pending input, and its run
  aborts.
- A child with pending user input, running or queued, keeps its run. Stop
  withdraws only the parent input that is still queued. A run that the parent
  already started continues, and its result can start a new parent turn after
  Stop.

Stop on the child itself ends all of its pending input. A replayed
`session_stop` call returns its first result and stops nothing new.

The recovery rule does not change. Before a Harness resumes, control stops each
pending parent turn in a child and marks it `interrupted`. Abort also withdraws
queued input. The parent gets one result. It names the interrupted turn and
the number of withdrawn queued inputs. Side effects of the stopped run are not
known, so dex does not run it again. The main agent can call `session_send` to
continue in the same context.

### Prompt rules

- Front desk and work loop: send a follow-up to the same child with
  `session_send`. Use `session_new` only for unrelated or parallel work. Use
  `session_fork` to branch from a child that already has the context. Remove
  the rule "cancel the task and spawn a new one".
- Read the full answer with `session_read` only when the result is cut. Use
  `session_search` and `session_read` to find earlier work and context in any
  session of the bot before asking the user to repeat it.
- Update `internal/engine/skills/coding/SKILL.md` to match.

### Web UI

A child session is a chat session, so the web UI shows it as one.

- The Sessions panel has two parts. "Needs you" lists questions, failed
  routine runs, and routines that need setup. The session list shows Chats and
  child sessions, newest activity first, with a running dot and a stalled hint.
  A child row shows the name of its parent.
- A failed child goes to its parent agent, not to "Needs you". The parent agent
  tells the user.
- Remove from the panel: task rows, the step preview, Stop, "Mark handled",
  "Mark all handled", the "Completed" group, and the local seen record.
- A child page is a normal chat page with the normal composer. It links to the
  parent. Remove `TaskSessionBar` and the read-only rule.
- Remove the task page and the "Task" kind. An old `/tasks/<id>` link opens the
  child session.
- The API replaces `Chat.task` (`TaskSummary`) with `Chat.parentId`. It removes
  the cancel, acknowledge, restore, and get task endpoints.

### Migration

1. Add `conversations.parent_id` and `submissions.parent_request_id`. Fill
   them from `tasks`.
2. After the web UI reads children from `parent_id`, drop `tasks` and
   `task_steer_receipts`.

## Consequences

- A follow-up uses the context that the child already has. The child does not
  read the repository again.
- The user and the agent use one set of session operations. The task layer
  (about 1,400 lines of Go and about 650 lines of web code, without tests) goes
  away.
- The structured report and `goalMet` go away. The main agent depends on the
  answer layout and its own checks.
- The user no longer triages failed tasks in the panel. This depends on the
  parent agent reporting each failed child result.
- The user and the parent can write to the same child. The PiG Inbox orders
  their input.
- Children stay until the user archives them. `session_list` shows only the 20
  newest. Long contexts depend on PiG compaction.
- This reverses the #99 statement that the first version does not reuse model
  context, and the one-root assertion in
  `internal/engine/task_binding_test.go`.

## Alternatives

- Add `message_task` and store each round as a task row. This keeps two models
  for one thing and adds code instead of removing it.
- Keep one-run tasks and let a new task read the reports of earlier tasks. Each
  task still reads the repository again.
