# CI and releases

## CI checks

[Check](../../.github/workflows/check.yml) runs on each push to `main` and each
pull request. Its jobs run the steps of `mise run check` in parallel: lint and
drift, web, Go lint and tests, and native checks and cross-builds.

Six app-test jobs partition all discovered top-level tests. Each job runs
its partition with the race detector. The Go job uses `check:go without-app`
for all other packages; local `check:go` still runs every package.
Run `mise run check:app-shard 0 6` to repeat the first partition locally.
Each app job saves its test list and JSON timings as a seven-day artifact.
The jobs share the main Go cache without saving duplicate archives.
The native job builds darwin and linux for amd64 and arm64 without cgo.
When you add a step to `check` in `mise.toml`, add it to the workflow too.

## Releases

To release:

1. Run `mise run release:bump <patch|minor|major|X.Y.Z|X.Y.Z-rc.N>` to update
   [VERSION](../../VERSION) and roll `[Unreleased]` in
   [CHANGELOG.md](../../CHANGELOG.md) into a dated section. A version with a
   hyphen, such as `0.17.0-rc.1`, makes a prerelease.
2. Review the changelog notes and open a pull request to `main`.

When a pull request updates [VERSION](../../VERSION) on `main`,
[Release](../../.github/workflows/release.yml) creates the `v<VERSION>` tag,
runs Check, and then runs [GoReleaser](../../.goreleaser.yaml) on Ubuntu.
GoReleaser builds `dex` for Linux and macOS, x64 and arm64, and uploads a
draft release with one `dex_<tag>_<os>_<arch>.tar.gz` for each platform,
`checksums.txt`, and notes from [CHANGELOG.md](../../CHANGELOG.md) (or from the
pull requests when no matching changelog section exists). GoReleaser builds the
web UI once and embeds it in each Go binary. Each binary starts on its own
platform as a smoke test. When all smoke tests pass, the workflow publishes the
draft. dex reads its version from the tag. Pushing a `v*` tag directly or
running `workflow_dispatch` also triggers the release workflow.

To try the build on your machine, run
`goreleaser release --snapshot --clean --parallelism 1`. The output goes to
`dist/`.

The macOS binaries are signed ad hoc, not notarized. macOS blocks a binary
from a browser download; run `xattr -d com.apple.quarantine dex` first.
