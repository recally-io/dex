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
the open or closed Context panel of a chat (`dex-context`), the bot rail
(`dex-sidebar`), and the Sessions sidebar (`dex-chats`). Inbox read and dismissed
state live in `control.sqlite`, not browser storage. The unsaved agent instructions are in
memory (`src/lib/app-state.tsx`), so a draft stays when the user opens another page, and a reload discards it.
`src/lib/agent.ts` runs agent saves one at a time. Each save reads the saved
agent first, so a model change does not overwrite the instructions.
Main and ordinary Chat sessions default to the agent's model and thinking level.
Saved session overrides apply from the next input, including channel input on Main.
Ended task conversations can also use model and project overrides.
New session sends `POST /api/agents/{agentId}/chats` with no request body.
When a registered Runner exists, a session with no messages shows Local and
the registered Runners (`GET /api/agents/{agentId}/runners`). Offline Runners are visible
but disabled. The project picker lists folders on the chosen Runner. After
the first message, the header shows its name. With other available Runners,
Fork from here offers a fork on the current Runner or another one.
See the [API contract](../api/openapi.yaml) for request and response schemas.
`src/lib/attachments.ts` uploads each file of the composer when the user adds
it; a message sends only upload IDs.

Bot links open Main. The bot header offers New session, Sessions, Automations,
and Settings. Settings has General, Skills, Integrations, and Secrets.
The right Sessions sidebar starts closed. The global sidebar
links to Board (`/board`) for Tasks and Inbox (`/inbox`) for owner actions.
Inbox reads active questions, unhandled failed or changed automation results,
missing models, and Tasks awaiting Owner review from `/api/inbox/summary`, and reads held events from the held events
endpoint. The Inbox link counts those items. One-way notifications stay in
transcripts and channels. Queued Task questions remain answerable from Task detail.
A held event shows its type, reason, author, source link, and text. The owner
chooses an open Task before Attach is enabled, because no API removes the object
links of an attach. The owner can also dismiss the event.
Board reads `/api/tasks/*`. It offers Kanban and List views, an inline
creation form, and Bot, In Review, Blocked, Source, and closed Task filters.
The Bot filter uses `/board?bot=<id>`. Task detail opens at
`/board/$taskKey` with a stable key such as `T-42`. Detail links to the parent, subtasks, and sibling dependencies. The Owner can create a subtask or change its dependencies there. Inbox review items link to the same detail route.

Task detail lists external runs next to Sessions. It shows the registered
Runner separately from the reported host and workspace. It also shows the
harness, branch, lease state, and heartbeat age. `/settings/runners` lists
active external Task runs by Runner. Both views read
`/api/tasks/executions`. Active runs refresh every five seconds because
heartbeats do not add Timeline entries.

`src/lib/task-stream.ts` uses EventSource to resume with `Last-Event-ID`.
It invalidates Task list and detail queries on Task events. Board does not
poll. Task edits keep the revision from the start of the edit. A conflict
prompts the Owner to load the latest values. Approval uses the current review
sequence. The composer adds a note or sends to the Assignee Bot's Main.
Global Integrations lists Sources from `GET /api/settings/sources` with a switch for each. A note asks for a
restart while a stored choice differs from what runs. When the GitHub plugin is on (`plugins` of `GET /api/health`), an open Task has
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
replaces the automation. Automations and Inbox use `continueResult`
(`lib/inbox.ts`). The new session pins the run's Local or Machine Runner and
uses its cwd. A run in a Sandbox, on an unavailable Runner, or with no recorded
Runner stops before it creates a session.
Each child session reads like a thread. The transcript shows one card at a
`session_new` or `session_fork` call, one line at a `session_send` call of a
parent, and a result line. The result has a "View session" link when the saved
result names a Session that the parent Bot can read.
A `session_report` call shows as a "Reported to parent" step. A click opens the
child as a read-only panel on the right, which covers the transcript on a narrow
screen. Task conversations are read-only while queued or running and writable
after they end.
The bot header opens the Sessions page and creates new sessions.
Main shows Stop only for its own native run target on a connected stream. A
retry sends the original target. A child can be active while Main sends an
ordinary input. Non-Main Stop keeps its legacy behavior.

Session activity lists scheduled and check Monitors and 30 recent Outbox
entries. A writable session manages Processes with `process_start`,
`process_list`, and `process_stop`. The panel does not list Processes. Read-only transcripts, including Automation runs, show the panel above
the transcript. The panel shows channel delivery status, not Web read status.
The panel shows the Monitor mode, attempt limit, and latest outcome. The user
can cancel an active Monitor in a writable session. Monitor creation uses the
session tool, not this panel.

Main and Chat display explicit Web-origin notifications in their
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
Skills and Integrations read `/api/agents/{agentId}/integrations` and both offer
Reload. In Integrations, an OAuth MCP row starts authorization with an
authenticated POST. The browser follows the returned URL in the same tab. The
callback returns to `/a/{agentId}/integrations`. The browser does not receive provider tokens.

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
