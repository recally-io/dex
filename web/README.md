# Web UI

The Go binary embeds the files in `dist/`. Build tasks generate these files;
Git does not track them.
The browser uses the API contract in [api/openapi.yaml](../api/openapi.yaml).
Do not edit the generated client in `src/api/generated/`.

## Development

1. Run `mise run install:web`.
2. Run `mise run dev:server` in one terminal.
3. Run `mise run dev:web` in another terminal.

The dev server sends `/api` requests to `http://127.0.0.1:8787`.
Set `DEX_API_URL` when the backend uses another address.

After an API change, run `mise run generate:api-web`.
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
Main and ordinary Chat sessions use the agent's model and thinking level.
New chat sends `POST /api/agents/{agentId}/chats` with no request body.
See the [API contract](../api/openapi.yaml) for request and response schemas.
`src/lib/attachments.ts` uploads each file of the composer when the user adds
it; a message sends only upload IDs.

The bot workspace has exactly four tabs: Chat, Routines, Extensions, Settings.
Bot links open Main. The right Chats sidebar starts closed. The global Inbox
sits above the bots and filters settled routine results by bot and read state.
Rail counts use the existing three-second agent poll. An open Inbox refreshes
its shared list every ten seconds. Result details can create a writable Chat
with the routine project and a quoted result through existing APIs.
Routine history links to run transcripts and Inbox results. Successful
`spawn_task` steps and task result marks link to read-only task detail pages.
There is no Sessions tab. Legacy Sessions and transcript URLs remain readable.

The old `/settings/<section>` addresses redirect to the new pages.

## Components and theme

The app uses the stock shadcn components in `src/components/ui/` and the preset
theme in `src/styles.css`. Domain components compose those primitives. Do not
copy a new atom or primitive. Add a component with the shadcn CLI when a stock
primitive is missing.

Every route renders one `PageHeader` from `src/components/page.tsx`. Bot pages
put the bot identity, tabs, and chat actions in that header. Global settings
pages use the `Settings` identity. The root has `data-page-header` so a future
desktop host change can target all headers together.

`src/components/dialog.tsx`, `confirm.tsx`, `shell.tsx`, and `icon-button.tsx`
own the application overlays. They compose the stock Dialog, Tooltip, and
related primitives. Keep focus management, Escape handling, and close-on-
navigation behavior in these domain wrappers.

`src/styles.css` is the only source of tokens. It contains the preset light and
dark semantic tokens, shared keyframes, and dex rules. The app uses JetBrains
Mono for body text and Merriweather for headings. It does not download fonts.

The theme follows the system until the user picks light or dark with the
toggle in the sidebar. `src/components/theme.tsx` applies the theme before the
first render.

`src/components/atoms/` and `src/components/primitives/` remain for routes that
are scheduled for later migration. Lint and format checks skip them. The app
parts are in `src/components/`: `page.tsx` for the header and framed page,
`form.tsx` for fields, `dialog.tsx` for modal dialogs, and `feedback.tsx` for
errors and loading states. Model and thinking pickers still own their current
keyboard search until their route migration.

The upstream copies import `cn`. The Vite and TypeScript aliases resolve that import
to `src/lib/utils.ts`, which uses the approved `clsx` and `tailwind-merge` packages.
The project does not need a separate `cn` package.

The install task puts a Go module boundary in `node_modules/`.
Some npm tools contain Go examples. This boundary keeps those examples out of
Go builds and lint checks.

### Stock shadcn foundation

`src/components/ui/` is the owned output of the stock shadcn/ui preset
`b4XK48HRFw` (`base-lyra`). Keep these files generic and compose them in domain
components. Do not add route behavior to this directory. The `components.json`
file records the preset, aliases, Base UI style, and Lucide icon library.

To update the foundation, review the preset first, then run the official CLI
from `web/`:

```sh
bunx --bun shadcn@latest apply b4XK48HRFw -y
bunx --bun shadcn@latest add button badge switch toggle-group dialog popover command tooltip skeleton separator scroll-area input textarea label -y
```

Review generated diffs before committing. Keep the compatibility aliases in
`src/styles.css` until all existing callers move to semantic tokens. Fonts are
bundled with `@fontsource-variable/jetbrains-mono` and
`@fontsource-variable/merriweather`, so the desktop app does not need a CDN.

Run `mise run check:web` after every foundation update. The check includes a guard for retired imports and token aliases.

Run `mise run check:foundation-browser /tmp/dex-foundation-evidence` to start a
real isolated app and Chrome profile. The task writes screenshots and
`result.json` to the given directory for Chat, Inbox, Inbox details, Routines,
routine history, the four-tab bar, bot Settings, and Providers at desktop and
mobile widths in light and dark modes. The browser also checks Inbox read,
dismissal, filters, and Continue in chat with the routine project.
