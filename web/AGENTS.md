# Web UI

The Go binary embeds the files in `dist/`. Build tasks generate these files;
Git does not track them.
The browser uses the API contract in [api/openapi.yaml](../api/openapi.yaml).
Do not edit the generated client in `src/api/generated/`.

## Development

1. Run `mise run install:web`.
2. Run `mise run dev:server` in one terminal.
3. Run `mise run dev:web` in another terminal.

The dev server sends `/api` requests to `http://127.0.0.1:18787`, the address of
`mise run dev:server`.
Set `DEX_API_URL` when the backend uses another address.

After an API change, run `mise run generate:api`.
Run `mise run check:web` to check types, lint, format, and the event reducer.
Set `DEX_E2E_DATA_DIR` to an empty directory to run browser tests with fresh data.
Git does not track `dist/`. Build and Go check tasks generate the embedded UI.
Run `mise run build:web` to build it separately.

## Pages

Each page in `src/routes/` reads the API only. The app keeps no settings in
`localStorage`. It stores only browser choices there: the theme (`dex-theme`),
the open or closed Context panel of a session (`dex-context`), the bot rail
(`dex-sidebar`), and the Sessions sidebar (`dex-chats`). Automation result read
and dismissed state live in `control.sqlite`, not browser storage. The unsaved bot instructions are in
memory (`src/lib/app-state.tsx`), so a draft stays when the user opens another page, and a reload discards it.
`src/lib/bot.ts` runs bot saves one at a time. Each save reads the saved
bot first, so a model change does not overwrite the instructions.
Main defaults to the Bot's model and thinking level. The composer offers model
overrides in writable sessions, including child sessions. These sessions and
Main can select a project directory.
New session sends `POST /api/bots/{botId}/sessions` with no request body.
A session detail page uses `/b/$botId/s/$sessionId`. The former `/a/` and
`/c/` routes have no redirect.
When a registered Runner exists, a session with no messages shows Local and
the registered Runners (`GET /api/bots/{botId}/runners`). Offline Runners are visible
but disabled. The project picker lists folders on the chosen Runner. After
the first message, the header shows its name. With other available Runners,
Fork from here offers a fork on the current Runner or another one.
See the [API contract](../api/openapi.yaml) for request and response schemas.
`src/lib/attachments.ts` uploads each file of the composer when the user adds
it; a message sends only upload IDs.

Bot links open Main at `/b/$botId`. The bot header offers Artifacts while it
shows Main or a Session, then New session, Sessions, Automations, and Bot
settings. Below `lg`, it shows Artifacts when available and a Bot menu. The menu
trigger names a failed latest Automation run and an unsaved instructions draft.
General settings open at `/b/$botId/bot`.
Settings also includes Skills, Integrations, and Secrets. A page outside a Bot
has no Bot actions in its header.
The right Sessions sidebar starts closed. The global sidebar
scrolls the Bots list above a fixed footer with [Board](../docs/dev/tasks.md#board),
Integrations, and Settings. The mobile drawer uses the same sidebar. Board (`/board`)
shows the Owner attention count and groups Tasks with other work:

- **Needs you**: active questions (answerable inline), Tasks awaiting Owner review (approvable inline), enabled Automations without an available model, and held external events (attach to an open Task or dismiss).
- **Blocked**: open Tasks with blockers. These Tasks do not add to the Owner attention badge.
- **Active**: open Tasks (`ready`, `in_progress`, and Bot-reviewed `in_review`).
- **Updates**: unhandled failed or changed Automation results grouped by Automation (`×N`), showing the latest summary preview and timestamp, expandable inline to read full text, mark done, or continue in a session (deep link at `/board/results/$runId`).
- **Later** (`backlog`) and collapsible **Completed** (`done`, `cancelled`).
  Empty sections are omitted. One-way notifications stay in transcripts and channels.
  The badge counts individual items in **Needs you** and **Updates**. Board reads `/api/tasks/*`, `/api/board/attention`, and `/api/sources/held-events`. Result details use `/api/automations/results`. A top quick-add bar creates a `ready` Task assigned to a Bot; Bot-scoped views use `/board?bot=<id>` with a dismissible chip.

Task detail opens at `/board/$taskKey` with a stable key such as `T-42`. It combines a structured property strip (clickable Status, Assignee Bot, and Reviewer controls that auto-save with `If-Match`, plus quick `+ Subtask` / `+ Dependency` / `+ Link PR` actions and an overflow `⋯` menu), a contextual action card when in review or blocked, dedicated context cards shown only when data exists (Sessions, external runs, parent/subtasks/dependencies, and external links/waits), a rail-structured Markdown Timeline, and a bottom composer defaulting to sending a message to the Assignee Bot's Main (staying on the Task page) with a secondary `Note only` action.
Active external runs refresh every five seconds because heartbeats do not add Timeline entries. `/settings/runners` also lists active external Task runs by Runner from `/api/tasks/executions`.

The shell uses `src/lib/board-attention.ts` for the Board badge and Bot status.
It uses `src/lib/task-stream.ts` to resume Task events with `Last-Event-ID`.
Task events invalidate the Task list, Task detail, and Board attention queries.
Board attention also polls every 10 seconds for changes outside the Task stream.
Task edits keep the revision from the start of the edit. A conflict
prompts the Owner to load the latest values. Approval uses the current review
sequence. The composer adds a note or sends to the Assignee Bot's Main.
Settings → Integrations lists Sources from `GET /api/settings/sources` with a switch for each. A note asks for a
restart while a stored choice differs from what runs.
Integrations (`/integrations?bot=<id>`) is the starting point for connections.
Without `bot`, it writes the last opened Bot, or the first Bot, into the address.
An unknown or archived Bot shows a notice and no Bot sections. Channels and MCP
servers use only the selected Bot's APIs. Sources stay global: the selection
does not change them. **Add MCP server** sends `POST /api/bots/{botId}/integrations/mcp`
with a name, a URL, and optional OAuth (client ID, a vault name for the client
secret, and scope). The dialog names the target Bot and takes no headers or tokens. When the GitHub plugin is on (`plugins` of `GET /api/health`), an open Task has
a form to link a GitHub PR by repository ID and PR number. Each plugin keeps its
components in `src/plugins/<name>/`, and a page renders them only when the
plugin is on.
An optional commit SHA sets a wait for a review submitted after the form is
submitted. The Task shows all linked Sessions above its external links, wait
states, and timeline. Each Session link uses the Session's Bot ID in its URL,
shows the Bot name and execution state, and marks the primary Session. A link shows its type, key, and namespace as
stored, for example `pull_request 7` in `github.com/repository/10`. Each timeline entry shows its author.
Automation history links to run transcripts and saved results.
The automation dialog can bind to a writable, unarchived session of the same bot.
It reads the eligible sessions from `listAutomationTargets`. In this mode it
shows the target picker instead of the Runner and project fields.
The automation dialog offers "Follow bot", Local, and SSH Machines. It keeps a
saved Runner that the list no longer shows. A change of Runner clears the
project directory. An automation update sends `runnerId`, because a PUT
replaces the automation. Board and Automations share result actions in
`src/lib/automation-results.ts`. The new session pins the run's Local or Machine Runner and
uses its cwd. A run in a Sandbox, on an unavailable Runner, or with no recorded
Runner stops before it creates a session.
Each child session reads like a thread. The transcript shows one card at a
`session_new` or `session_fork` call, one line at a `session_send` call of a
parent, and a result line. The result has a "View session" link when the saved
result names a Session that the parent Bot can read.
A `session_report` call shows as a "Reported to parent" step. A click opens the
child as a read-only panel on the right, which covers the transcript on a narrow
screen. The full child Session page stays writable while the child runs.
Transcript items distinguish `session_result`, `wake`, and `task_wake`.
A Runner change in a fork projects as `boundary: "runner"`.
The bot header opens the Sessions page and creates new sessions.
Main shows Stop only for its own native run target on a connected stream. A
retry sends the original target. A child can be active while Main sends an
ordinary input. In other writable sessions, Stop also withdraws queued input.

Session activity lists scheduled and check Monitors and 30 recent Outbox
entries. A writable session manages Processes with `process_start`,
`process_list`, and `process_stop`. The panel does not list Processes. Read-only transcripts, including Automation runs, show the panel above
the transcript. The panel shows channel delivery status, not Web read status.
The panel shows the Monitor mode, attempt limit, and latest outcome. The user
can cancel an active Monitor in a writable session. Monitor creation uses the
session tool, not this panel.

Main and Web sessions display explicit Web-origin notifications in their
transcripts. The client merges the live notification pages and older pages by
notification ID. The native transcript and notifications have separate cursors;
**Show earlier messages** loads both. Channel and Automation delivery records stay
in the Outbox panel.

The About page (`/settings/about`) manages server operations through `/api/system/*`: switching the persisted update channel (`stable` or `prerelease`), checking releases, running an in-place upgrade with automatic `control.sqlite` backup, restarting the server gracefully (with wait-until-idle, cancel, and force-now controls), taking manual hot backups, and reading recent entries from the in-memory log ring buffer. When a restart or upgrade begins, it polls `GET /api/system/status`. A change in `startedAt` confirms the restart and refreshes health data.

Runners (`/settings/runners`) shows the built-in Local Runner, Machines, and
Sandboxes. Machines use SSH or Outbound. The page shows each Outbound Machine's
online state, platform, and version alignment against `GET /api/health`.
Runner settings use `/api/settings/runners`. The Runner editor lets the owner
set or clear an optional plain-text description.
Global Secrets (`/settings/secrets`) edits shared Vault entries and 1Password.
A bot's Secrets page edits its own Vault entries. The vault API returns metadata
only. The editor clears the value after submit and does not store it in the browser.
[Skills and Integrations](../docs/dev/skills-and-integrations.md) read
`/api/bots/{botId}/integrations` and both offer
Reload. An MCP row shows its connection state and, for OAuth, its authorization
state as separate badges. Authorized does not mean connected. Dex-level rows
mark authorization as shared by the Bots that inherit the declaration. In Integrations,
an OAuth MCP row starts authorization with an
authenticated POST. The browser follows the returned URL in the same tab. The
callback returns to `/b/{botId}/integrations`. The browser does not receive provider tokens.

The Telegram page under Integrations uses the existing token form. It also offers
managed creation when the API confirms Bot Management Mode for the saved manager.
The managed dialog polls its setup state. It shows a private owner verification
link first and the Telegram bot creation link second. Cancel calls the API.
Neither the managed dialog nor the Channels response receives the created token.

`/settings` opens `/settings/general`.

## Components and theme

The app uses the stock shadcn components in `src/components/ui/` and the preset
theme in `src/styles.css`. Domain components compose those primitives. Do not
copy a new atom or primitive. Add a component with the shadcn CLI when a stock
primitive is missing.

Every route renders inside `FramedPage` or `MainPane` from `src/components/page.tsx`.

`src/components/dialog.tsx`, `confirm.tsx`, `shell.tsx`, and `icon-button.tsx`
own the application overlays. They compose the stock Dialog, Tooltip, and
related primitives. Keep focus management, Escape handling, and close-on-
navigation behavior in these domain wrappers.

`src/styles.css` is the single source of semantic tokens, shared keyframes, and dex styles. Code blocks and monospace labels use `@fontsource-variable/jetbrains-mono` without a CDN.

The theme follows the system until the user picks light or dark with the toggle in the sidebar. `src/components/theme.tsx` applies the theme before the first render.

The install task puts a Go module boundary in `node_modules/`. Some npm tools contain Go examples. This boundary keeps those examples out of Go builds and lint checks.

### Stock shadcn foundation

`src/components/ui/` is the owned output of the stock shadcn/ui preset `b4XK48HRFw` (`base-lyra`). Keep these files generic and compose them in domain components. Do not add route behavior to this directory. The `components.json` file records the preset, aliases, Base UI style, and Lucide icon library.

To update the foundation, review the preset first, then run the official CLI from `web/`:

```sh
bunx --bun shadcn@latest apply b4XK48HRFw -y
bunx --bun shadcn@latest add button badge switch toggle-group dialog popover command tooltip skeleton input textarea label -y
```

Run `mise run check:web` after every foundation update. The check includes a guard for retired imports and token aliases.
