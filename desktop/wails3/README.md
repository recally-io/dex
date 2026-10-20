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
