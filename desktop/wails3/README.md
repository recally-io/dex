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

## Release

The release workflow runs `mise run package:desktop <tag>` from the repository
root. The task builds arm64 and amd64 binaries, joins them with `lipo`, sets the
bundle version from the tag, and writes
`dist/desktop/Dex_<tag>_macos_universal.zip`. The workflow starts each slice,
checks that `/api/health` reports the tag, and uploads the zip to the release.

The app has an ad-hoc signature and is not notarized. macOS reports a
downloaded copy as damaged. Run this once after you move it to Applications:

```sh
xattr -cr /Applications/Dex.app
```

Go 1.27 builds for macOS 13 or later, so the app requires macOS 13.

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

The first version does not add a second API contract. Developer ID signing and
notarization are not done yet.

## Native macOS shell

The app installs a native macOS menu bar with Dex, File, Edit, View and Window
menus. Settings… (Cmd+,) opens `/settings`. New Chat (Cmd+N) starts a chat for
the open bot, or for the last bot, from any page. Wails supplies the standard
edit roles, so the message box keeps native copy and paste behavior.

The window uses the default macOS title bar. It owns the traffic lights, the
title, window drags and double-click zoom, so no page has to leave room for
them. A title bar drawn by the web UI needs one shared header for every route
and width first; that is part of the UI work in #42.

The window loads Dex from the backend URL, not from the Wails asset server, so
Wails injects only its core script. That script never reports ready, and Wails
queues every `ExecJS` call until the page does. The shell reports ready itself
when a navigation finishes, so the menu reaches the web app through the
`dex:settings` and `dex:new-chat` window events. The web app needs no other
knowledge of the shell.

The Chat project dialog has a Finder button in the app. The page posts
`dex:pick-folder {id, start}` to the WKWebView `external` message handler. The
shell accepts it only from the main frame of the Dex backend origin, shows the
native folder panel, and answers with the `dex:folder-picked` window event. The
chosen path is saved through the normal project API, with its validation. A
browser has no handler, so the button does not show there.

The main window frame is saved as JSON in the platform config directory
(`~/Library/Application Support/Dex/window.json` on macOS). The file stores
position, size and the maximized state; a maximized window keeps its last
normal frame. The window starts hidden and is placed when the application
starts, because Wails has no screen list before then. A frame that does not
intersect any current screen is discarded, and the default centered 1280×860
frame is used.
