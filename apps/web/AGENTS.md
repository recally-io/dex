# Web UI

## Development

Follow [Development](../../docs/dev/development.md) to run the server and the
Web UI. The server and the native app embed `dist/`; Git does not track it.
`public/assets` points to `apps/assets/`. Keep only public files there.

[OpenAPI](../../api/openapi.yaml) owns the HTTP contract. After a contract
change, run `mise run generate:api`. Do not edit `src/api/generated/`.
Run `mise run check:web` for types, lint, format, and Web tests.

## Routes and state

`src/main.tsx` registers routes. `src/routes/` contains page components.

- `/` opens `/b/default`. `/b/$botId` opens Main. `/b/$botId/s/$sessionId`
  opens a Session. The bot header opens `/sessions`, `/automations`, or `/bot`
  below the Bot path. Bot settings link to `/integrations`, `/channels`, and
  `/secrets` under the same path. The `/skills` redirect opens the Skills
  section of Bot Integrations.
- `/board`, `/board/$taskKey`, and `/board/results/$runId` show the
  [Board and Task views](../../docs/dev/tasks.md#board).
- `/integrations` shows shared skills and MCP declarations. `section` selects
  `skills` or `mcp`. `add=skill` opens catalog search; `add=mcp` opens the
  MCP dialog. `target` preselects a Bot. An invalid target blocks submission.
- `/settings` opens `/settings/general`. Settings pages use `/providers`,
  `/runners`, `/secrets`, `/integrations`, `/archived`, and `/about` under
  `/settings`.

Server settings and Automation result state stay on the server. `localStorage`
keeps theme and sidebar choices. `src/lib/app-state.tsx` keeps unsaved Bot
instructions in memory by Bot ID. A full reload discards them.
`src/lib/session-draft.ts` uses `sessionStorage` for a one-time composer draft
handoff. `src/lib/bot.ts` serializes saves and fetches the saved Bot before
each write, so one save does not overwrite another field with stale data.

`src/lib/session-stream.ts` consumes the engine's transcript projection.
Main can keep its composer mounted while hidden. Browser tests must skip
`[inert]` subtrees. `src/lib/attachments.ts` uploads files as the Owner adds
them; a message contains only upload IDs. The [session contract](../../docs/dev/sessions.md)
owns Stop, fork, and result behavior. [Execution](../../docs/dev/execution.md)
owns the event and history format. The Sessions list checks the clock every
15 seconds. A busy Session is **Stalled** if a tool has run for more than
30 minutes, or if no tool is running and activity is more than 5 minutes old.

`src/lib/task-stream.ts` resumes Task events with `Last-Event-ID` and
invalidates the Task list, detail, and Board attention. `src/lib/board-attention.ts`
also polls every 10 seconds for changes outside the stream. Task edits keep
the revision from edit start; a conflict asks the Owner to load current
values. Approval uses the current review sequence.

Plugin UI belongs in `src/plugins/<name>/`. Render it only when `/api/health`
lists the plugin. Automation history and Board share result actions in
`src/lib/automation-results.ts`. See the [Automation result contract](../../docs/dev/automations.md#results).

The About page calls `/api/system/*`. During a restart or upgrade, it polls
status. A changed `startedAt` confirms the restart. The native app exposes
workspace switching through `dex-workspace://settings`; `DexWorkspace/1` in
the user agent controls display. The link opens the connection screen and
does not select a server or clear a login.

## Components and theme

Compose the stock shadcn components in `src/components/ui/` in domain
components. Keep route logic outside `ui/`. Do not add new primitives,
direct icon packages, or token aliases. Place pages in `FramedPage` or
`MainPane` from `src/components/page.tsx`. Use the wrappers in
`src/components/dialog.tsx`, `confirm.tsx`, `shell.tsx`, and `icon-button.tsx`
for focus, Escape, and navigation behavior.

`src/styles.css` owns semantic tokens and shared styles. Use its tokens and
the local JetBrains Mono font. `src/components/theme.tsx` applies the system
theme until the Owner chooses a theme.

Review `components.json` and the stock `b4XK48HRFw` (`base-lyra`) preset before
updating the foundation. Use the official CLI from `apps/web/`:

```sh
bunx --bun shadcn@latest apply b4XK48HRFw -y
bunx --bun shadcn@latest add button badge switch toggle-group dialog popover command tooltip skeleton input textarea label -y
```

Review the generated diff. Run `mise run check:web`; it includes the import
and token guard.
