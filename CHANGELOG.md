# Changelog

All notable changes to `dex` will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [v0.20.0-rc.3] - 2026-11-23

### Changed

- **Feishu**: Use the official Lark Go SDK (`v3.13.0`) for tenant tokens, IM message/reaction requests, and WebSocket protobuf frames while preserving Dex's serialized socket ACK, streaming resource download, and per-client token cache (#275).

### Fixed

- **Remote Machines**: Keep bot leases across transient runner reconnects without forcing a reload, replace half-open sessions immediately on same-token reconnect, wait up to 10 seconds before dispatch for a reconnecting runner, wake waiting chats immediately on reconnect, and scope extension/environment reloads to the target agent so another busy bot cannot block recovery (#276).

### Removed

- **Web**: Remove the Feedback button and `/feedback` slash command from the Web chat composer. Channel `/feedback` commands remain unchanged (#274).

## [v0.20.0-rc.2] - 2026-11-23

### Added

- **Server operations**: Use **Settings → About** to check for an update, select a release channel, upgrade or restart the server, back up `control.sqlite`, and view recent logs. Upgrades create a database backup before replacing the binary (#267).
- **Runner updates**: A runner with a writable binary can download the matching version from Dex and restart when it reconnects. Existing Linux runners installed at `/usr/local/bin/dex-agent` need a manual migration to enable this feature. See [Remote machines](docs/remote-machines.md#automatic-version-alignment) (#268).

### Changed

- **Runner installation**: New installs run as the account that runs the install command. Tasks can use that account's tools, files, and credentials. On Linux, add `--dedicated` to use a separate account. Existing installations do not migrate automatically (#272).
- **Release checks**: Build all four platform archives. Run the release smoke tests on Linux amd64, Linux arm64, and macOS arm64.

### Fixed

- **Tool schemas**: Encode an empty required list as `[]` (#271).

### Upgrade and rollback

- Keep a backup of `control.sqlite` from before the upgrade. To roll back to v0.19.x after plugin migrations apply, restore that backup before starting the old version. This discards database changes made after the backup.

## [v0.20.0-rc.1] - 2026-11-23

### Added

- **Task Inbox & Lifecycle**: Add the Task Inbox, lifecycle states, Main-routed owner messages and queued Task questions, `task_*` tools with primary Session binding, Main alerts for new Tasks and alert waits, held-event triage for conflicting or unmatched source events, and source-agnostic Task links (#208, #212, #222, #225, #226, #231).
- **Plugin System & In-Process Plugins**: Add plugin enable/disable settings and plugin-owned database migrations (#235, #243), with built-in plugins for **GitHub** (PR/issue/review/check-suite intake via webhooks and scheduled pulls; #217, #220, #227, #232), **Webhook** (signed Task event sources; #237), and **Email** (forwarded email intake and IMAP folder pulling; #239, #240).
- **Bounded Monitors**: Replace session timers with bounded monitors supporting one-shot and recurring shell checks within the target session's Bash environment, output-change detection, and queued-event withdrawal on cancellation (#259).
- **Routines**: Bind Routines to existing sessions (#265) and alert through the channel Outbox when a Routine fails to start (#264).
- **Channels & Feedback**: Show channel input status with message reactions (#258), track explicit channel follow-up commitments with stop/clear/revocation cleanup (#262), and prepare public feedback drafts without calling a model (#255).
- **Execution Environments**: Allow sessions to select any registered execution environment (#249).

### Changed

- **API & Monitors**: Replace `/api/agents/{agentId}/chats/{chatId}/timers` (and `followup_timers`) with `/api/agents/{agentId}/chats/{chatId}/monitors` (and `monitors`) (#259).
- **Remote Machines**: Load the Runner login shell `PATH` at `dex-agent` startup (#254).
- **Control**: Consolidate channel delivery bookkeeping (#246) and process child reports before silent System Turn exit (#263).
- **Docs & Packaging**: Split `docs/dev/architecture.md` into per-subsystem documents and organize packaging and documentation assets (#238, #244).
- **Upgrade & Rollback**: Back up `control.sqlite` before upgrading. Once plugin-owned migrations apply, automatic database downgrade is blocked; rolling back to `v0.19.x` requires restoring the pre-upgrade database backup.

### Fixed

- **Remote Machines**: Allow multiple bots to share a runner under `workspaces/<bot-id>/workspace` in the runner state directory while leaving legacy `workspace/` files in place (also shipped in `v0.19.1`) (#230, #233).
- **Database**: Apply skipped `main` migrations when upgrading from a `v0.19.1` release-branch database (#242).
- **Tests & CI**: Run app tests in one non-race CI job and stabilize runner-recovery and environment-switch integration tests (#229, #234, #236).

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

- **Always-on Inbox & Batching**: Add `background_start(wake="none")`, in-turn `background_wait`, local-only child background wakes, and `FollowUpMode: durable.QueueAll` with atomic `InboxFollowUp` origin isolation (#186, #187).
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

[Unreleased]: https://github.com/recally-io/dex/compare/v0.20.0-rc.3...HEAD
[v0.20.0-rc.3]: https://github.com/recally-io/dex/compare/v0.20.0-rc.2...v0.20.0-rc.3
[v0.20.0-rc.2]: https://github.com/recally-io/dex/compare/v0.20.0-rc.1...v0.20.0-rc.2
[v0.20.0-rc.1]: https://github.com/recally-io/dex/compare/v0.19.1...v0.20.0-rc.1
[v0.19.0]: https://github.com/recally-io/dex/compare/v0.18.0...v0.19.0
[v0.18.0]: https://github.com/recally-io/dex/compare/v0.17.0...v0.18.0
[v0.17.0]: https://github.com/recally-io/dex/compare/v0.16.1...v0.17.0
[v0.16.1]: https://github.com/recally-io/dex/compare/v0.16.0...v0.16.1
