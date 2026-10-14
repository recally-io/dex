# 0001: Embed a compiled Bun worker in the Go binary

Status: Accepted (2026-10-14)

## Context

dex runs agents on pi-durable, which is TypeScript. Control is Go. The host
must need only the dex binary, with no Node or Bun installed.

## Options

1. Embed the Bun runtime and the worker JavaScript, and run `bun run`.
2. Embed one `bun build --compile` executable.
3. Write control in TypeScript too, with one process.

## Decision

Option 2, gzipped. Measured on darwin-arm64: the Bun runtime is 60 MB and the
worker bundle is 0.4 MB, so option 1 saves almost nothing and adds a second
file to manage. gzip (standard library) shrinks the worker to about 30 MB;
zstd would save more but needs a dependency.

Control talks to the worker over stdin and stdout with one JSON object per
line. The pipe ties the worker's life to control's: when control dies, the
worker's stdin closes and it exits. There is no socket file and no token.

Bun 1.4 supports `node:sqlite`, so the worker uses pi-durable's own SQLite
storage. `worker/test/storage.test.ts` runs pi-durable's storage conformance
suite on Bun to keep that true.

## Consequences

- Each target platform needs its own worker build (`GOOS`/`GOARCH` select the
  Bun target in `build:worker`).
- pi-durable is tested upstream on Node only. A Bun difference outside storage
  shows only in our tests.
- The worker must keep stdout for protocol lines; it sends console output to
  stderr.
- The worker build disables `.env` and `bunfig.toml` autoloading, because its
  working directory can hold agent-written files.
