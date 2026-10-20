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
the open or closed Context panel of a chat (`dex-context`), and the automation
unread marker (`dex-automations-seen`). The unsaved agent instructions are in
memory (`src/lib/app-state.tsx`), so a draft stays when the user opens another page, and a reload discards it.
`src/lib/agent.ts` runs agent saves one at a time. Each save reads the saved
agent first, so a model change does not overwrite the instructions.
Main and ordinary Chat sessions use the agent's model and thinking level.
New session sends `POST /api/agents/{agentId}/chats` with no request body.
See the [API contract](../api/openapi.yaml) for request and response schemas.
`src/lib/attachments.ts` uploads each file of the composer when the user adds
it; a message sends only upload IDs.

The old `/settings/<section>` addresses redirect to the new pages.

## Components and theme

The look follows Beautiful UI by
[slev12397/beautiful-ui](https://github.com/slev12397/beautiful-ui) (MIT).
`src/styles.css` is the only source of tokens. It has the upstream light and
dark tokens from `app/globals.css`, the shared keyframes, and a few dex rules.
The app uses system font stacks with Inter and JetBrains Mono as the first choices.
It does not download fonts.

The theme follows the system until the user picks light or dark with the
toggle in the sidebar. `src/components/theme.tsx` applies the theme before the
first render.

`src/components/atoms/` and `src/components/primitives/` are upstream copies.
Lint and format checks skip them. A dex change in a copy has a comment at the
top of the file. The app parts are in `src/components/`: `page.tsx` for the
framed page, sections and fact rows, `form.tsx` for fields, `dialog.tsx` for
modal dialogs, and `feedback.tsx` for errors and loading states. The dialogs,
the drawer, and the model and thinking pickers use Base UI directly. The pickers
use cmdk for the keyboard and the model search.

The upstream copies import `cn`. The Vite and TypeScript aliases resolve that import
to `src/lib/utils.ts`, which uses the approved `clsx` and `tailwind-merge` packages.
The project does not need a separate `cn` package.

The install task puts a Go module boundary in `node_modules/`.
Some npm tools contain Go examples. This boundary keeps those examples out of
Go builds and lint checks.
