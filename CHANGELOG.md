# Changelog

All notable changes to `dex` will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [v0.20.0-rc.7] - 2026-11-26

### Added

- **Task Board**: Add the unified Task ledger (`T-N` keys, Board and Task detail views, Needs you attention queue, live SSE updates), Task decomposition (subtasks and sibling dependencies), cross-Bot handoffs, multi-bot/multi-session links, and external runs (`dex task`, lease tokens, `/api/tasks/*`) (#337, #341, #347, #348, #349, #355).
- **Channels & Integrations**: Add Telegram managed bot setup (#320), optional inbound webhooks for Telegram and Feishu (#319), owner-scoped channel management tools (#317), and browser OAuth for remote HTTP MCP servers (#321).
- **Browser E2E**: Add main-path end-to-end browser coverage across Bots, Sessions, Board/Tasks, Automations, and Settings, and restore active Bot and Session navigation state (#366).

### Changed

- **Canonical naming & routes**: Align database, API, CLI, and Web UI terminology to **Bots** (`/api/bots*`, `/b/$botId`), **Sessions** (`/api/bots/{id}/sessions*`, `/b/$botId/s/$sessionId`), **Runners** (`local`, `runner`, `ssh`, `e2b`), **Automations**, **Secrets**, **Skills**, and **Integrations**, and reorganize Settings (#333, #338, #339, #340, #350, #352, #362, #364, #365).
- **Execution & delivery architecture**: Unify inbound receipts on `submissions`, split `session_result` / `wake` / `task_wake` receipts, move Outbox delivery ownership to `outbox_messages` (one platform message per delivery), route completed runs by last placed user input, split `process_*` tools from `monitor_set`, split `task_sessions` and `task_external_runs`, and admit Sessions while a cold default Runner opens (#314, #315, #316, #322, #324, #326, #327, #328, #329, #330, #335, #357, #359, #361, #362).
- **CI & test performance**: Cache a real migrated SQLite template for fresh test databases, parallelize isolated tests, and split `Engine race tests` (#367).

### Removed

- **Telegram group chats & legacy compatibility**: Retire Telegram group chats (sealing pending work on upgrade), follow-up commitment tracking, `/api/inbox/*`, retired child tool aliases, and pre-Board compatibility columns and paths (#323, #331, #336, #353, #356).

### Upgrade and rollback

- This release applies schema migrations since `v0.20.0-rc.6` (Task Board tables, `agents` to `bots`, `conversations` to `sessions`, `task_executions` split, Outbox and Runner cleanups, and plugin table renames) while keeping on-disk bot directories (`<dataDir>/agents/<id>/`) in place. Back up the data directory before upgrading. To roll back to `v0.20.0-rc.6`, stop Dex and restore the pre-upgrade backup. This discards changes made after the backup.

## [v0.19.0] - 2026-11-21

### Added

- **Remote Machines**: Connect runners to the controller over outbound WebSockets, with a pinned TLS listener (#198).
- **Execution Environments**: Dispatch work through environment backends and leases. Session tools, Routines, and session forks can run in a selected execution environment (#199, #202, #205, #206).

### Changed

- **Web**: Rename `Tasks` and `New task` to `Sessions` and `New session`. Existing URLs keep their routes (#207).
- **Docs**: Remove ADRs and make `docs/dev/architecture.md` the source of truth (#200).
- **Control**: Simplify the service environment and definitions (#195).

### Fixed

- **Sessions**: Wait for a queued steer before the child's tool is released (#204).
- **Tests**: Stabilize asynchronous App integration tests (#197).

## [v0.18.0] - 2026-11-19

### Added

- **Always-on Inbox & Batching**: Add a silent background command mode, in-turn waits, local-only child command wakes, and `FollowUpMode: durable.QueueAll` with atomic `InboxFollowUp` origin isolation (#186, #187).
- **Unified Outbox & Fail-Quiet System Turns**: Add `outbox_messages` and `outbox_files`, fail-quiet System Turn exit contracts (`notify_user`, `report_parent`, `stay_silent`, `system_error` fallback), `recent-outbox` prompt continuity, and folded silent System Turn traces in the Web UI (#188).
- **Bounded Follow-up Timers & Routine Outbox**: Add session-scoped `followup_timers` (`timer_set`, `timer_list`, `timer_cancel`, `delay >= 60s`, `horizon <= 7d`, `chain_depth <= 6`, max 32 active per session) and cut Routine alerts over to `outbox_messages` (#189).
- **Web UI Observability**: Add `GET/DELETE .../timers` and `GET .../outbox` APIs with a chat toolbar Timers & Outbox popover (#191).

### Fixed

- **Feishu**: Atomically revoke pending Feishu `outbox_messages` and `submissions` on credential deletion or `app_id`/`owner_id` change, and add isolated delivery/recovery boundary tests (#192).

### Changed

- **CI**: Shard `internal/app` race tests into 6 partitions and simplify mise tasks (#181, #182).

## [v0.17.0] - 2026-11-18

### Added

- **ExecEnv**: Decouple control plane from execution environment, adding `pi-env` and E2B sandbox lifecycle support (#168, #169, #171, #173).
- **Settings**: Add default bot configuration under `/settings` (#175).

### Changed

- **Web**: Move settings pages under `/settings` (#175).
- **Dev**: Run `dev:server` on port `18787` with `~/.dex-dev` (#176).

### Removed

- **Tests**: Replace legacy browser test scripts with `e2e` (#178).
- **Treewide**: Remove unused code, abstractions, and redundant tests (#177).

## [v0.16.1] - 2026-11-17

### Changed

- **Telegram**: Use Rich Messages for all new text delivery (#165).

### Removed

- **Telegram**: Remove legacy text and delivery shims (#166).

[Unreleased]: https://github.com/vaayne/dex/compare/v0.20.0-rc.7...HEAD
[v0.20.0-rc.7]: https://github.com/vaayne/dex/compare/v0.20.0-rc.6...v0.20.0-rc.7
[v0.19.0]: https://github.com/recally-io/dex/compare/v0.18.0...v0.19.0
[v0.18.0]: https://github.com/recally-io/dex/compare/v0.17.0...v0.18.0
[v0.17.0]: https://github.com/recally-io/dex/compare/v0.16.1...v0.17.0
[v0.16.1]: https://github.com/recally-io/dex/compare/v0.16.0...v0.16.1
