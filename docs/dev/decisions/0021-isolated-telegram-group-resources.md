# 0021: Isolated Telegram group execution resources

Status: Proposed. Refs #159. The user has selected separate group resources and a minimal tool allowlist.

## Context

The private agent constructor loads owner instructions, memory, AGENTS files, skills, tool environment and MCP connections. A different conversation key cannot isolate those resources. Hiding tool schemas cannot stop nested or deferred execution.

A group reader need not be an authorized sender. Sender authorization cannot grant access to the private bot profile or owner controls.

## Decision

Keep the real bot identity. Add a trusted resource audience to native `engine.Ref` and creation options. An empty scope keeps the existing private runtime. A canonical `tg_<Telegram bot ID>_<negative chat ID>_<topic ID>` selects the group constructor. Scope comes from trusted platform routing and persisted business data, never from model arguments or message text.

Each audience has separate execution storage at `agents/<id>/telegram-groups/<scope>/state.sqlite` and a separate `resources/` directory. Native IDs are local to that storage; callers must retain both scope and native ID. Regular group sessions can separate senders inside one resource audience. Topic sessions can share their topic audience. There is no new real bot or general ACL language.

The group constructor does not call the private resource loader, open private memory, capture the login environment, discover MCP, or set a project. It installs a fixed executable registry. Group configuration discards private instructions/CWD and replaces extension/tool selections. Settlement does not commit private memory. The constructor installs the restricted registry before the Harness can resume stored work.

The registry permits only `group_resource`, `group_note` and the application-bound `group_deliver`. A native before-tool hook rejects all other names at dispatch. The hook also applies to nested and deferred calls and task-owned children. The resource handlers hold a scope-rooted filesystem handle. They reject traversal and symlinks. Reads are limited to explicitly published UTF-8 resources up to 64 KiB or PNG/JPEG/GIF/WebP images up to 20 MiB. Delivery reads only the same audience root and queues frozen bytes through the existing source-bound outbox. It never uses the private artifact directory. Notes use immutable operation-owned files, separate from private memory. Forks and engine recovery retain the scope.

Control persists the execution scope and immutable audience/session binding on each conversation. Each submission records the exact account/chat/topic/sender identity. Native ID lookup, recovery, cancellation, live views and history use that stored scope. Generic private input cannot reopen a group conversation with an empty scope. Private bot/session/Routine handlers and the bot-wide artifact directory are not group sharing paths. Group input does not feed private dream consolidation.

The application checks current account and sender authorization at admission, before every provider request, before tool effects and before public delivery. It checks every input of a steered run. PiG v0.7 reports ordinary generation hook errors and continues. A denied request must use its native task abort mark, with the invocation context, so the scheduler cancels the request before provider dispatch. A hook error alone is not an authorization barrier.

## Rollout boundary

The adapter accepts group/supergroup input only from a current paired actor who mentions the bot or replies to it. Ordinary groups use sender sessions; topics, including General without a thread ID, share their topic session. The transport checks current authorization before downloads. Uploads, album collection, cleanup and output retain bot/chat/topic ownership. Stop/clear/new use only the parsed session; Main cannot discard group albums. Group speech stays off and retains original audio. Group drafts/previews stay off.

These changes and fake-platform tests do not enable a real owner bot or authorize deployment. Existing private instructions must not enter through these paths. Menus, Questions, callbacks and permitted child workflows remain unfinished.

Menus, Questions, callbacks and child workflows must use narrow scope-bound handlers before their tool names can enter the fixed group registry. Private bot management, Routines, process tools, generic filesystem tools, MCP discovery and cross-bot session/memory tools stay absent.

## Verification and limits

Use real native execution and SQLite. Adversarial native registries can offer and register forbidden executables; tests must prove that the execution hook blocks them, not only that the model cannot see them. Remove that hook through a Go overlay to prove that direct, nested/deferred and task-owned-child calls execute without it.

Context canaries, real local MCP configuration, separate audience files, traversal/symlink attempts, forks and reopen tests cover resource initialization and recovery. Equal-native-ID history/live probes, private owner/source-confusion probes, current-actor revocation and private artifact delivery probes cover the business boundary. Real local Bot API boundaries cover mention/reply triggers, sender/topic/General routing, current pairing before downloads, scoped stop/clear, album/file ownership, native image content, output threads and private speech non-inheritance. Removing the pre-download grant check makes a revoked actor fetch a real local file. Removing Main's album filter discards another topic's pending upload. These tests do not authorize a real Telegram group or prove the unfinished workflow paths. The runtime boundary is not an OS sandbox against the local owner.
