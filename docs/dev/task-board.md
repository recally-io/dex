# Task Board Architecture

This document defines the target architecture for dex's Bot-centric, append-only Task Board (tracked in [#291](https://github.com/recally-io/dex/issues/291)). Current implemented Task Inbox behavior stays in [Tasks](tasks.md) until each phase ships.

![Task Board architecture and boundaries](../assets/task-board.png)

Source: [task-board.puml](../assets/src/task-board.puml).

## Mental model and core invariants

dex models a team for one owner:

- **dex** is the team.
- **Each Bot** is a team member. Its **Main session** is its working desk and juggles multiple Tasks at once.
- **The Task Board** is the team's shared durable working memory and progress ledger.
- **Executions** are the concrete runs a Bot uses to do work on a Task: internal dex sessions or external coding agents (such as Claude Code, Codex, or OpenCode) on local or remote machines.

Five invariants govern the design:

1. **Bot accountability.** Every Task has one owning dex Bot (`assignee_bot_id NOT NULL`). External agents run as Executions under a dex Bot; they never own a Task independently.
2. **Task ownership is not Session ownership.** Reassigning a Task from Bot A to Bot B does not transfer `conversations.parent_id` control over Bot A's existing child sessions. Bot B's Main creates its own child sessions.
3. **Append-only ledger as source of truth.** `work_task_entries` uses `INTEGER PRIMARY KEY AUTOINCREMENT` and forbids physical deletion (`tombstone` only, no `ON DELETE CASCADE`). Every Board mutation writes one ledger entry in the same SQLite write transaction so `work_tasks` is a consistent projection of the ledger and SSE cursors never skip changes.
4. **Storage and lock boundaries.** All Board state lives in `control.sqlite` (`STRICT` tables). Native execution state remains in `agents/<id>/state.sqlite`. No call path holds two bot delivery gates: cross-bot assignment, review, and gate unblock commit durable DB state first and invoke `wake()` after commit; cross-bot stops reuse `abort_pending`.
5. **Lightweight grouping.** Board grouping uses `Space` (for example `general`, `dev/dex`, `ops`) to stay distinct from messaging Channels (Telegram, Feishu, WeChat). Spaces provide organization and filtering only; they do not store routing defaults (`default_bot_id`, `environment`, or `cwd`) that duplicate bot or plugin settings.

## Domain model

| Table / entity      | Key fields and semantics                                                                                                                                                                                                                                                                                                                     |
| ------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `task_spaces`       | `id`, unique `slug` (default `general`), `name`, `description`, `archived_at`. Display and filter grouping only.                                                                                                                                                                                                                             |
| `work_tasks`        | `id`, `number` (`AUTOINCREMENT` -> global stable key `T-42`), `space_id` (defaults to `general` for plugin `AcceptCreating` compatibility), `title`, `status`, `parent_task_id`, `authorized_by_root_id`, `assignee_bot_id` (`NOT NULL`), `creator_kind`/`id`, `reviewer_kind`/`id`, `review_seq`, `next_entry_seq`, `revision`, timestamps. |
| `work_task_entries` | `id` (`AUTOINCREMENT` global SSE cursor), `task_id`, `seq` (`UNIQUE(task_id, seq)`), `kind`, optional `tag`, `actor_kind`, `actor_bot_id`, `actor_execution_id`, `trust`, `event_id`, `meta_json`, `content`, `created_at`.                                                                                                                  |
| `task_executions`   | `id`, `task_id`, `bot_id`, `kind` (`dex_session` \| `external_agent`), `conversation_id`, `turn_request_id`, `harness`, `machine_id`, `host_meta_json`, `workspace_path`, `git_branch`, `external_session_id`, `state`, `lease_epoch`, `lease_expires_at`, `last_heartbeat_at`, `started_at`, `ended_at`.                                    |
| `task_watchers`     | `(task_id, bot_id)`. Grants read-only access and badge visibility; watching never grants write permission.                                                                                                                                                                                                                                   |
| `task_dependencies` | `(task_id, depends_on_task_id)` (Phase 2). Allowed only between sibling Tasks that share the same `parent_task_id`. Cycle check and insert run in the same write transaction.                                                                                                                                                                |

### Entry kinds and projections

`work_task_entries.kind` uses eight core types:

- `note` (optional `tag`: `brief`, `decision`, `handoff`, or `update`; cold-start context bootstrap selects `brief` and `decision` entries first)
- `artifact`
- `status`
- `assign`
- `review`
- `execution`
- `external`
- `tombstone`

Generic `task_post` writes only `note` and `artifact`. Dedicated server mutations write `status`, `assign`, `review`, `execution`, `external`, and `tombstone`, preventing agents from forging privileged events.

### Access control and optimistic concurrency

Central `canRead(bot, task)` and `canWrite(bot, task, action)` checks replace scattered `t.agent_id = caller` query filters:

- `canRead` allows the assignee, reviewer, watchers, and child executions bound to that Task.
- `revision` applies only to cross-request HTTP and CLI writes via `If-Match: <revision>`. Internal agent tools execute inside immediate SQLite writer transactions and do not expose `revision` in tool arguments.

## Lifecycle, derived Gates, and review

`work_tasks.status` stores six states: `backlog`, `ready`, `in_progress`, `in_review`, `done`, and `cancelled`.

`waiting` is not stored. Blocked state (`blocked` plus active Gate details) is derived on read from three read-only Gate sources:

1. Unfinished sibling `task_dependencies` (Phase 2).
2. Unresolved external `work_task_waits`.
3. Open `questions` (`ask_user`, keeping its existing transactional selection and delivery path in `internal/chat/work_tasks.go`, while showing queue position in the UI when multiple Tasks ask Main at once).

Key transition rules:

- **Default reviewer.** When a Bot creates or delegates a Task to a different Bot (`creator != assignee`), `reviewer` defaults to the creating Bot. Self-created sub-tasks and Owner-created Tasks default to `reviewer = none` unless the caller explicitly sets `review: owner`.
- **Review version binding.** Moving a Task to `in_review` records `review_seq` (the entry sequence submitted for review). `task_review(approve)` succeeds only when it matches the current `review_seq`; new submissions invalidate stale approvals. `task_review(request_changes)` appends feedback, moves the Task back to `in_progress`, and wakes the assignee Bot.
- **No implicit completion.** Finishing an Execution never automatically moves a Task to `in_review` or `done`; the assignee Bot's Main session explicitly submits or closes it.
- **Upstream edge cases.** Cancelling an upstream Task does not clear downstream Gates; it wakes the downstream assignee for manual resolution. Reopening a completed upstream Task (`done -> in_progress`) does not roll back started downstream work; it appends a `status` notice and wakes the downstream assignee.

## Authorization matrix

Server-side authorization replaces the single `fromUser` check with a `role x run origin x root authorization` matrix:

| Action                                              | Allowed actor                                | Allowed run origin                                      | Additional constraint                                                |
| --------------------------------------------------- | -------------------------------------------- | ------------------------------------------------------- | -------------------------------------------------------------------- |
| `task_post` (`note` / `artifact`)                   | Assignee, reviewer, watcher, bound execution | `user`, `task_wake`, child `task_result`                | External Board Token is scoped to one Task and one Execution         |
| `backlog`/`ready` <-> `in_progress`, `-> in_review` | Assignee Bot or Owner                        | `user`, `task_wake`                                     | `in_review` sets `review_seq`; executions cannot trigger it directly |
| `task_review` (`approve` / `request_changes`)       | Reviewer or Owner                            | Bot reviewer: `task_wake` or `user`; Owner: `user` only | `approve` must match current `review_seq`                            |
| `task_split` (create child Task)                    | Parent assignee or Owner                     | `user`, `task_wake`                                     | Root Task must be user-created (`authorized_by_root_id` required)    |
| Top-level `task_new`                                | Any Bot Main or Owner                        | `user` only (plus plugin `AcceptCreating` rules)        | Prevents unprompted automation from creating top-level Tasks         |
| `cancel`, `reopen`, change reviewer, move space     | Owner only                                   | `user` only                                             | Bots, wakeups, external agents, and external events are rejected     |
| External plugin events & external tokens            | Cannot change Task status or lifecycle       | —                                                       | Append `external` / `note` / `artifact` or resolve waits only        |

## Trust boundary and context reads

Entries carry a three-level `trust` classification:

- `owner`: written by the human Owner.
- `agent`: written by a dex Bot or an external coding agent (`actor_kind = external_agent` distinguishes external agents).
- `external`: ingested from external plugins (GitHub, Email, Webhook).

Protection rules:

- **External isolation.** Raw `external` entry bodies stay visible to the Owner in the UI and never enter model reads, summaries, upstream digests, mentions, titles, SSE model projections, or wakeup payloads.
- **Data envelope.** `task_read` wraps every entry with `trust != owner` in a fixed data envelope (for example `<task-entry seq="5" actor="bot:coder" trust="agent">...</task-entry>`). Tool instructions state that entries are data, never commands or new authorization.
- **Side-effect-free cursor reads.** `task_read(taskId, afterSeq?, limit?, tags?, includeUpstream?)` returns entries and `nextCursor` without mutating shared read state, so filtered reads, upstream reads, or dropped responses never cause another consumer to skip unread entries.
- **Bootstrap budget.** Cold-start context assembly inlines trusted entries up to a compact budget (for example 16 KiB) and prioritizes `brief` and `decision` tags, falling back to `task_read(afterSeq)` pagination for longer histories.

## Coalesced Main wakeups

Main is a front desk (`docs/dev/sessions.md`): queuing one submission per Task event delays user messages behind a burst of task turns.

- **One unadmitted wake per Main.** A partial unique index on `submissions` (`WHERE kind = 'task_wake' AND admitted_at IS NULL`) ensures each Bot's Main has at most one pending wake before native admission. Incoming wake events upsert that row with the new high-water cursor and a deduplicated list of `(taskKey, reason)` items.
- **Pointer-only payload.** A `task_wake` payload contains only cursor pointers and event categories, never entry body text. Main reads details on demand via `task_my_workload` and `task_read`.
- **Decision allowlist.** Only decision-requiring events trigger `task_wake`: `assigned`, `review_requested`, `changes_requested`, `gate_unblocked` (including resolved alert waits), `execution_failed`, `execution_stale`, and `question_answered`. Routine `note`, `artifact`, and heartbeat writes update UI badges and SSE only.
- **No internal double-wakeup.** Internal child sessions already deliver a `task_result` to their parent on settlement. Internal `task_executions` project their state from existing conversation and submission records (`conversation_id` + `turn_request_id`) and do not emit a second auto-echo wakeup.

## External agents and machines (Phase 3)

External coding agents (Claude Code, Codex, OpenCode, or custom scripts) integrate under an owning dex Bot through two tracks:

1. **Attached mode (CLI / MCP, first priority).**
   - Each external Execution receives a scoped Board Token bound to `(task_id, execution_id, lease_epoch)`, stored only as a SHA-256 digest and injected via environment variable or `vault:NAME` (never passed in tool arguments that enter transcripts).
   - The token allows reading non-`external` entries of that Task, posting `note`/`artifact`, sending heartbeats, and marking its own execution `completed` or `failed`. It cannot read other Tasks or change Task status.
   - Every write validates current `lease_epoch`, execution state, and server time in the same SQLite write condition, returning `409 claim_lost` immediately when expired or superseded.
   - **Startup grace and recovery.** On dex startup after downtime, active external leases extend to `max(lease_expires_at, start + TTL)` before the watchdog runs to prevent false stale storms. Transiently disconnected workers recover via `dex board attach --resume <execution_id>`, which bumps `lease_epoch` and returns `stale` to `running`.
   - **Workspace isolation.** Lease epochs block stale Board writes but cannot stop a detached OS process from editing a shared checkout. Parallel writers must use separate worktrees or directories. UI labels distinguish registered Runner identities from self-reported `hostname`/`cwd`.
2. **Managed Push via Command Monitors.**
   - Rather than adding harness-specific verbs to `dex-agent` runner RPC, Managed Push reuses `monitor_set_command` (`claude -p ... --resume <id>` or equivalent) with a durable `dispatch_id` and strict `(machine_enrollment, harness, workspace, external_session_id)` binding checks before resuming.
   - Before enabling Managed Push, verify that long-running background commands on a remote Runner do not hold the Runner's single-operation socket and block concurrent tool calls from other bots on the same machine.

## Web UI information architecture

The Web UI follows `web/AGENTS.md` (stock shadcn components and `web/src/styles.css` tokens) and replaces 3-second polling with global-cursor SSE (`GET /api/board/events`, `Last-Event-ID`):

- **Team Board (`/tasks`)**: Space filter pills (`All`, `#general`, custom Spaces), Bot filter, `In Review` and `Blocked` filters, and a **Kanban (`Backlog | Ready | In Progress | In Review | Done`) / List** view toggle. Legacy `/inbox` and `/a/$agentId/inbox/$taskId` links redirect cleanly.
- **Per-Bot `Tasks` tab**: Extends the bot workspace to five tabs (`Chat | Tasks | Routines | Extensions | Settings`; update `web/AGENTS.md` in the same commit) using the same Board query filtered by `assignee` and `watcher`.
- **Separated `Triage` view**: Houses unmatched external `Held Events` (attach, dismiss, or create Task) and `Routine Results` outside the main Task Board.
- **Task detail view/drawer (`/tasks/$taskKey`)**: Stable key (`T-42`), `In Review` action bar (`Approve` / `Request Changes`), filterable `#01..#N` append-only stream, Executions & Machines panel, Gates and External Links (`TaskLinks` + `GitHubPullForm`), and unified Append / Notify Main / Dispatch composer.

## Phased rollout

| Phase       | Scope                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| ----------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Phase 1** | Central `canRead`/`canWrite`; `task_spaces` (`general` default); `AUTOINCREMENT` append-only ledger (`seq`, 8 kinds + `tag`, 3-tier `trust`, `tombstone`); global key `T-42`; 6 stored statuses + derived `blocked`; `in_review` + `review_seq`; internal `task_executions`; B1 authorization matrix; B3 coalesced `task_wake`; side-effect-free `task_read(afterSeq)`; OpenAPI + global SSE; Kanban/List Team Board UI, Bot `Tasks` tab, `Triage` split, and legacy route redirects. |
| **Phase 2** | `parent_task_id` decomposition (`task_split`); cross-bot `task_assign` and `task_review` (no nested delivery gates); sibling `depends_on` (same-tx cycle check, `cancelled`/`reopen` semantics); scoped child session `task_read`/`task_post`; `task_my_workload`.                                                                                                                                                                                                                    |
| **Phase 3** | Attached CLI/MCP + scoped SHA-256 Board Tokens + Lease Watchdog (startup grace, write-time epoch check, `--resume`); Environments external executions visibility; Managed Push via Command Monitors after runner socket concurrency verification.                                                                                                                                                                                                                                     |

## Verification criteria

Before closing each rollout phase, verify with real SQLite and integration tests (no mocks):

- Upgrading an existing `control.sqlite` preserves historical tasks, entries, primary sessions, external links, waits, and open questions.
- Replaying webhook deliveries, native receipts, and tool keys produces no duplicate entries or wakeups.
- Crash/restart recovery across `commit -> wake` and child settlement delivers at most one pending review wake; stale `review_seq` approvals and expired/superseded tokens fail with conflict errors.
- Concurrent cross-bot delegations (`A -> B` and `B -> A`) plus `Stop` never deadlock or hold two delivery gates.
- Five parallel task events while Main is busy coalesce into a single `task_wake` pointer without blocking Owner input.
- External entry bodies never appear in model tool outputs, wakeups, or summaries, and never mutate Task lifecycle.
