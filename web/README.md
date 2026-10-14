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
`localStorage`; it stores only the theme and the automation unread marker
there. The unsaved agent instructions are in memory (`src/lib/app-state.tsx`),
so a draft stays when the user opens another page, and a reload discards it.
`src/lib/agent.ts` runs agent saves one at a time. Each save reads the saved
agent first, so a model change does not overwrite the instructions.

The old `/settings/<section>` addresses redirect to the new pages.

## Components and theme

The shadcn components use Base UI and the `base-nova` style.
Beautiful UI supplies the theme and the agent components.
The tokens came from
[slev12397/beautiful-ui](https://github.com/slev12397/beautiful-ui/blob/main/app/globals.css).
The app uses system font stacks with Inter and JetBrains Mono as the first choices.
It does not download fonts.

Edit `src/styles.css` for project styles.
Treat `src/styles/shadcn.css`, `src/components/ui/`, and `src/components/atoms/`
as registry output.
The registry imports `cn`. The Vite and TypeScript aliases resolve that import
to `src/lib/utils.ts`, which uses the approved `clsx` and `tailwind-merge` packages.
The project does not need a separate `cn` package.

The install task puts a Go module boundary in `node_modules/`.
Some npm tools contain Go examples. This boundary keeps those examples out of
Go builds and lint checks.
