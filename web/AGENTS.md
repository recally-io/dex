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
Git does not track `dist/`. Build and Go check tasks generate the embedded UI.
Run `mise run build:web` to build it separately.

## Pages

Each page in `src/routes/` reads the API only. The app keeps no settings in
`localStorage`. It stores only browser choices there: the theme (`dex-theme`),
the open or closed Context panel of a chat (`dex-context`), the bot rail
(`dex-sidebar`), and the Chats sidebar (`dex-chats`). Inbox read and dismissed
state live in `control.sqlite`, not browser storage. The unsaved agent instructions are in
memory (`src/lib/app-state.tsx`), so a draft stays when the user opens another page, and a reload discards it.
`src/lib/agent.ts` runs agent saves one at a time. Each save reads the saved
agent first, so a model change does not overwrite the instructions.
Main and ordinary Chat sessions default to the agent's model and thinking level.
Saved session overrides apply from the next input, including channel input on Main.
Ended task conversations can also use model and project overrides.
New chat sends `POST /api/agents/{agentId}/chats` with no request body.
When an SSH environment exists or the bot is not local, a chat that has no
message yet shows an environment picker (`listAgentEnvironments`), and the project
picker lists folders in the chosen environment. After the first message the
header shows the environment name. With other available environments, Fork from here
opens a menu that forks in the current environment or to another one.
See the [API contract](../api/openapi.yaml) for request and response schemas.
`src/lib/attachments.ts` uploads each file of the composer when the user adds
it; a message sends only upload IDs.

The bot workspace has exactly four tabs: Chat, Routines, Extensions, Settings.
Bot links open Main. The right Chats sidebar starts closed. The global sidebar
links to `/inbox`, which lists Tasks, held events, and Routine results across bots.
A held event shows its type, reason, author, source link, and text. The owner
chooses an open Task before Attach is enabled, because no API removes the object
links of an attach. The owner can also dismiss the event.
`/a/{agentId}/inbox/{taskId}` opens a Task only under the bot that owns it.
On an open Task, the owner can link a GitHub PR by repository ID and PR number.
An optional commit SHA sets a wait for a review submitted after the form is
submitted. The Task shows its primary Session link, then its linked PRs and
wait states, above the timeline. Each timeline entry shows its author.
Routine history links to run transcripts and saved results.
The routine dialog offers "Follow bot", `local`, and the SSH environments. It
keeps a saved environment that the list no longer shows, and a change of the
environment clears the project directory. A routine update sends
`environmentId`, because a PUT replaces the routine. Routines and Inbox continue
a result through `continueResult` (`lib/inbox.ts`): the new chat pins `local` or
the SSH environment of the run and uses its cwd. A run in an E2B or runner
environment, or with no recorded environment, stops before any chat is created.
Each child session reads like a thread. The transcript shows one card at a
`session_new`, `session_fork` or `spawn_task` call, one line at a `session_send`
call, and one "replied · View thread" line at each result mark. A click opens the
child as a read-only panel on the right, which covers the transcript on a narrow
screen. Task conversations are read-only while queued or running and writable
after they end.
The bot header opens the Sessions page and creates new sessions. Legacy Sessions and transcript URLs remain readable.

The old `/settings/<section>` addresses redirect to the new pages.

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
