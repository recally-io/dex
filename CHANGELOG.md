# Changelog

All notable changes to `dex` will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [v0.20.0] - 2026-11-27

This release covers all changes since `v0.19.1`, including `v0.20.0-rc.1` to `v0.20.0-rc.7`.

### Added

- **Task Board**: Add a Task ledger with `T-N` keys, Board and Task detail views, a Needs you attention queue, and live SSE updates. Tasks support subtasks, sibling dependencies, cross-Bot handoffs, links to several Bots and Sessions, and external runs through `dex task`, lease tokens, and `/api/tasks/*` (#208, #212, #222, #225, #226, #231, #337, #341, #347, #348, #349, #355, #357, #361).
- **Plugins**: Turn plugins on and off in Settings. Each plugin owns its database migrations. Built-in plugins: **GitHub** (pull requests, issues, reviews, and check suites through webhooks and scheduled pulls), **Webhook** (signed Task event sources), and **Email** (forwarded email and IMAP folder pulls) (#217, #220, #227, #232, #235, #237, #239, #240, #243).
- **Integrations**: Add an Integrations hub. Add a custom remote HTTP MCP server to a Bot, connect it with browser OAuth, and find Skills and MCP servers through presets and registry search. Google Workspace MCP presets request offline access (#321, #370, #375).
- **Secret vault**: Store shared and Bot secrets in an encrypted vault. Pass `vault:NAME` references to Bash, command Monitors, and MCP servers (#288).
- **Server operations**: In **Settings → About**, check for updates, select a release channel, upgrade or restart the server, back up `control.sqlite`, and view recent logs. An upgrade backs up the database before it replaces the binary (#267, #287).
- **Runner updates**: A runner with a writable binary downloads the matching version from Dex and restarts when it reconnects. Existing Linux runners at `/usr/local/bin/dex-agent` need a manual migration. See [Remote machines](docs/remote-machines.md#automatic-version-alignment) (#268).
- **Monitors and processes**: Replace session timers and `background_*` tools with bounded Monitors (`monitor_set`) and `process_*` tools for long-running commands (#259, #286, #335).
- **Channels**: Add Telegram managed bot setup, optional inbound webhooks for Telegram and Feishu, owner-scoped channel management tools, and input status reactions (#258, #317, #319, #320).
- **Automations**: Bind Automations to existing Sessions and alert through the Outbox when a run fails to start (#264, #265).
- **Runners**: Sessions can select any registered Runner. Runners have an optional description, which `runner_list` returns as `desc` (#249, #295).
- **Feedback**: Prepare public feedback drafts without a model call (#255).

### Changed

- **Canonical naming**: Align the database, API, CLI, and Web UI to **Bots** (`/api/bots*`, `/b/$botId`), **Sessions** (`/api/bots/{id}/sessions*`, `/b/$botId/s/$sessionId`), **Runners** (`local`, `runner`, `ssh`, `e2b`), **Automations**, **Secrets**, **Skills**, and **Integrations**. Settings has a new layout (#333, #338, #339, #340, #350, #352, #362, #364, #365).
- **Delivery**: Inbound receipts use `submissions`. Outbox owns delivery with one platform message per delivery. Session results, wakes, and Task wakes have separate receipts. Completed runs route to the last placed user input (#246, #314, #315, #316, #322, #324, #326, #327, #328, #329, #330, #359, #362).
- **Session admission**: Each Session has its own admission queue and retry backoff, so a stalled child does not hold Main or other Sessions. A Session is admitted while a cold default Runner opens (#310, #314).
- **Remote Machines**: Run up to 16 operations at the same time on one Runner connection. Keep Bot leases across short reconnects. Load the login shell `PATH` at `dex-agent` startup (#254, #276, #309, #310).
- **Runner installation**: New installs run as the account that runs the install command. On Linux, add `--dedicated` to use a separate account. Existing installations do not migrate (#272).
- **Control storage**: Store MCP server declarations and managed Skills on the control host. Use the `mcp_server_*` tools to manage MCP servers. Import legacy `AGENTS.md` files into Bot instructions on startup, and keep the source files with a `.migrated` suffix (#283, #284, #285).
- **Extension reloads**: Reload an idle Bot when supported settings or tools change. Manual file changes need a reload or restart (#289).
- **Tools**: Enable `tool_search` in owner sessions without MCP. Rename `read_dream_message` to `memory_source_read`. Rename `report_parent` to `session_report`, which takes only `content` and `final` (#290, #302).
- **Web Main**: Main offers Send while its children work. Main Stop cancels only the foreground task and keeps pending inputs, child Sessions, processes, and Monitors (#306).
- **Compaction**: Compact the session context locally without a model call (#293).
- **Bash**: The default `bash` tool timeout is 600 seconds instead of 120 seconds (#310).
- **Feishu**: Use the official Lark Go SDK (`v3.13.0`) for REST and WebSocket frames (#275).
- **PiG**: Upgrade to v0.8.0 (#373).

### Removed

- **Telegram**: Remove group chats (pending work is sealed on upgrade), the `telegram_message` tool, and agent speech output. Normal replies, previews, reactions, and audio transcription are unchanged (#323).
- **System turns**: Remove `stay_silent`. Top-level system turns finish without a message by default. Delegated child turns return their results.
- **Compatibility**: Remove follow-up commitment tracking, `/api/inbox/*`, retired child tool aliases, and pre-Board compatibility columns and paths (#331, #336, #353, #356).
- **Web**: Remove the Feedback button and `/feedback` slash command from the Web composer. Channel `/feedback` commands are unchanged (#274).

### Fixed

- **Database**: Apply `main` migrations that a `v0.19.1` release-branch database skipped (#242).
- **MCP**: Refuse a saved OAuth declaration that has no settings, and show the owner how to replace it (#372).
- **Runner**: Keep reading the legacy `environmentId` in `dex-agent` `device.json` (#368).
- **Tool schemas**: Encode an empty required list as `[]` (#271).

### Upgrade and rollback

- **Back up the data directory before you upgrade.** This release applies many schema migrations, including plugin migrations, the Task Board tables, and the renames of `agents` to `bots` and `conversations` to `sessions`. Bot directories (`<dataDir>/agents/<id>/`) stay in place.
- **Rollback needs the backup.** Automatic database downgrade is blocked after the plugin migrations apply. To roll back to `v0.19.x`, stop Dex and restore the pre-upgrade backup. This discards changes made after the backup.
- Dex ignores legacy `.env` and `mcp.json` files at the dex and Bot levels and does not import them. Move secrets into the vault and register MCP servers again. See [Skills and Integrations](docs/dev/skills-and-integrations.md).

## [v0.19.1] - 2026-11-21

### Fixed

- **Remote Machines**: Allow multiple bots to share a runner. Each bot starts in `workspaces/<bot-id>/workspace` under the runner state directory. Dex leaves files in the old `workspace/` directory in place (#230).
  - **Rollback to v0.19.0**: Back up `control.sqlite` before the upgrade. Replacing the binary alone does not restore the single-bot limit. Before starting v0.19.0, restore the pre-upgrade database backup or reduce each runner to at most one active bot and apply the database downgrade. Restoring the backup discards database changes since the backup. v0.19.0 uses `workspace/` again and does not move files from the new directories.

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

[Unreleased]: https://github.com/recally-io/dex/compare/v0.20.0...HEAD
[v0.20.0]: https://github.com/recally-io/dex/compare/v0.19.1...v0.20.0
[v0.19.1]: https://github.com/recally-io/dex/compare/v0.19.0...v0.19.1
[v0.19.0]: https://github.com/recally-io/dex/compare/v0.18.0...v0.19.0
[v0.18.0]: https://github.com/recally-io/dex/compare/v0.17.0...v0.18.0
[v0.17.0]: https://github.com/recally-io/dex/compare/v0.16.1...v0.17.0
[v0.16.1]: https://github.com/recally-io/dex/compare/v0.16.0...v0.16.1
