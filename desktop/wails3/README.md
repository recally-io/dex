# Dex Wails 3 desktop build

This directory contains the macOS desktop entry for Dex.

The Wails window loads the Dex HTTP server on a loopback port. The React UI keeps
using the existing OpenAPI and SSE contracts. Wails bindings and Streams are not
used for transcript delivery.

The module pins Wails CLI, Go module, and generated runtime behavior to
`v3.0.0-beta.33`. The nested module keeps this beta dependency out of Dex's main
module and non-macOS builds.

## Build

Run these commands from this directory:

```sh
mise exec -- env GOBIN=/tmp/dex-wails3-bin go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.33
PATH=/tmp/dex-wails3-bin:$PATH mise exec -- go mod tidy
PATH=/tmp/dex-wails3-bin:$PATH mise exec -- wails3 build
PATH=/tmp/dex-wails3-bin:$PATH mise exec -- wails3 package
```

The build runs `mise run build:web` first. It writes the macOS binary to
`bin/dex-wails3`. The package task wraps that binary in `bin/Dex.app` and
applies an ad-hoc signature for local testing. The binary embeds the same
`web/dist` assets through the Dex `web` package.

## Development

```sh
PATH=/tmp/dex-wails3-bin:$PATH mise exec -- wails3 dev -config ./build/config.yml
```

The development command rebuilds the web UI before it starts the window. The
current beta file watcher can fail in restricted environments. Run this command
in a normal macOS terminal when testing Wails development reloads.

## Runtime boundary

The desktop process first probes the configured Dex service. If it finds a
healthy service with the same data directory, the window uses that service. If
no such service exists, the desktop process starts Dex on `127.0.0.1:0`, waits
for the listener, and loads the resulting URL in the Wails window. Closing the
last window shuts down only a backend started by the desktop process. A Wails
single-instance lock prevents two desktop processes from sharing the same data
directory.

When it reuses an existing service, the desktop process starts a loopback
proxy. The proxy adds the stored service token to backend requests and rewrites
the origin for Dex's same-origin write protection. The WebView does not receive
the token and does not show the token sign-in flow.

The first version does not add a second API contract. The next desktop work must
verify loopback startup, SSE reconnects, token handling, app signing, and
notarization before release.

## Native macOS shell

The app installs a native macOS menu bar with Dex, File, Edit, View and Window
menus. Settings… (Cmd+,) opens `/settings`. New Chat (Cmd+N) starts a chat for
the open bot, or for the last bot, from any page. Wails supplies the standard
edit roles, so the message box keeps native copy and paste behavior.

The window loads Dex from the backend URL, not from the Wails asset server. Wails
then injects only its core script. That script can post messages to Go, but it
does not report ready and has no drag handling. Two consequences:

- Wails queues every `ExecJS` call until the page reports ready. The shell
  reports ready itself when a navigation finishes, so the menu can reach the
  web app through `dex:settings` and `dex:new-chat` window events.
- The web app starts window drags itself (`web/src/lib/mac-shell.ts`). Elements
  with `--dex-drag: drag` move the window; controls inside them opt out.

The web app detects the shell from `DexDesktop` in the user agent, before its
first render, and sets `document.documentElement.dataset.shell = "mac"`. One
CSS block in `web/src/styles.css` keys off that hook. The window has a
translucent backdrop and a hidden inset title bar. The sidebar's first row and
the pane header form one 52px bar, in which macOS centers the traffic lights.
The collapsed sidebar is 80px wide on macOS so that the lights fit inside it.
Browser sessions keep the existing look.

The main window frame is saved as JSON in the platform config directory
(`~/Library/Application Support/Dex/window.json` on macOS). The file stores
position, size and the maximized state; a maximized window keeps its last
normal frame. The window starts hidden and is placed when the application
starts, because Wails has no screen list before then. A frame that does not
intersect any current screen is discarded, and the default centered 1280×860
frame is used.
